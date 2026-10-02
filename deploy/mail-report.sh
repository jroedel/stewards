#!/usr/bin/env bash
#
# How cron and mail are set up on the hosting account, without a secret in it.
#
# Run ON the server by `deploy/deploy.sh mail-report` (make prod-mail-report),
# which pipes this file to `bash -s` over ssh. It is never installed there and
# changes nothing: every line below reads.
#
# Why it exists (2026-10-02): cron's emails from this account land in Gmail's
# spam, with no DKIM signature and From root@<host>, while the apps' own mail,
# sent through an authenticated relay, does not. Deciding what to change --
# MAILTO, MAILFROM, a mailbox for cron, a forward, or silence -- needs the
# account's actual settings, and four or five applications share it, so the
# answer has to cover every job in the crontab and not only this app's.
#
# What it never prints. A password is reported as set or empty. Settings files
# are read for named keys only (host, port, from, user), never as a whole. And
# everything printed from a crontab or a forward goes through redact(), which
# blanks key=value pairs that look like credentials, the password in a URL,
# a --secret <value> flag, mysql's -p<password>, and any long unbroken token, because a cron line is exactly where somebody
# once pasted an API key.
#
# No `set -e`: a probe that fails -- no exim here, a file not readable -- is a
# finding to report, not a reason to stop.
set -uo pipefail

APP_DIR="${1:-}"

section() { printf '\n== %s\n' "$*"; }

# home shows a path under the home directory as ~/..., which is shorter and
# leaves the account's full path out of a report that may be pasted around.
# Through a function because a literal ~ in a ${x/#...} replacement is itself
# expanded back to $HOME by bash.
home() { local t='~'; printf '%s' "${1/#$HOME/$t}"; }
none()    { printf '   (%s)\n' "$*"; }
indent()  { sed 's/^/   /'; }

# redact blanks what might be a credential. Deliberately greedy: a redacted
# path costs a reader a guess, and a printed key costs a rotation.
redact() {
	sed -E \
		-e 's/((pass(word|wd)?|secret|token|api[_-]?key|auth|key)[[:space:]]*[=:][[:space:]]*)("[^"]*"|'"'"'[^'"'"']*'"'"'|[^[:space:]]+)/\1[redacted]/Ig' \
		-e 's#(://[^/:@[:space:]]+:)[^@[:space:]]+@#\1[redacted]@#g' \
		-e 's/(^|[[:space:]])-p[^[:space:]]+/\1-p[redacted]/g' \
		-e 's/(--?(pass(word|wd)?|secret|token|api[_-]?key|auth|key)[[:space:]]+)[^[:space:]]+/\1[redacted]/Ig' \
		-e 's/[A-Za-z0-9_+=-]{24,}/[redacted]/g'
}

# domains collects every mail domain the report meets, for deploy.sh to look
# up in DNS on the person's own machine afterwards.
DOMAINS=()
domain_of() {
	[[ "$1" == *@* ]] || return 0
	local d="${1##*@}"
	d="${d%%[>\"\' ]*}"
	[[ "$d" == *.* ]] && DOMAINS+=("${d,,}")
}

# ------------------------------------------------------------------ the account

section "the account"
printf '   user:     %s\n' "$(id -un)"
printf '   host:     %s\n' "$(hostname -f 2>/dev/null || hostname)"
HOSTNAME_FQDN="$(hostname -f 2>/dev/null || hostname)"
domain_of "x@$HOSTNAME_FQDN"

# ------------------------------------------------------------------ cron

section "the crontab (redacted)"
CRONTAB="$(crontab -l 2>/dev/null)"
if [ -z "$CRONTAB" ]; then
	none "no crontab, or it could not be read"
else
	printf '%s\n' "$CRONTAB" | redact | indent
fi

section "who cron emails, and from what address"
MAILTO="$(printf '%s\n' "$CRONTAB" | sed -nE 's/^[[:space:]]*MAILTO[[:space:]]*=[[:space:]]*"?([^"]*)"?.*/\1/p' | tail -1)"
MAILFROM="$(printf '%s\n' "$CRONTAB" | sed -nE 's/^[[:space:]]*MAILFROM[[:space:]]*=[[:space:]]*"?([^"]*)"?.*/\1/p' | tail -1)"
if printf '%s\n' "$CRONTAB" | grep -qE '^[[:space:]]*MAILTO[[:space:]]*='; then
	printf '   MAILTO:   %s\n' "${MAILTO:-(empty: cron sends no mail at all)}"
	[ -z "$MAILTO" ] || domain_of "$MAILTO"
else
	printf '   MAILTO:   not set, so cron mails the account itself (%s)\n' "$(id -un)"
fi
if [ -n "$MAILFROM" ]; then
	printf '   MAILFROM: %s\n' "$MAILFROM"
	domain_of "$MAILFROM"
else
	printf '   MAILFROM: not set, so the From is the default (root or the user, at the host)\n'
fi

# Which cron this is decides whether MAILFROM is even honoured: cronie reads
# it, Debian's cron does not.
CRON_IS="unknown"
for c in /usr/sbin/crond /usr/sbin/cron; do
	[ -e "$c" ] || continue
	CRON_IS="$c -> $(readlink -f "$c")"
done
if command -v rpm >/dev/null 2>&1 && rpm -q cronie >/dev/null 2>&1; then
	CRON_IS="$CRON_IS (cronie: MAILFROM works)"
elif command -v dpkg-query >/dev/null 2>&1 && dpkg-query -W cron >/dev/null 2>&1; then
	CRON_IS="$CRON_IS (Debian cron $(dpkg-query -W -f '${Version}' cron 2>/dev/null): MAILFROM is ignored)"
fi
printf '   cron:     %s\n' "$CRON_IS"

section "which jobs email on every run"
# A job emails whenever it prints. One whose output is not sent anywhere is
# quiet only if the command itself prints nothing when all is well, which
# this cannot know -- so "may email" is the honest word for those.
if [ -z "$CRONTAB" ]; then
	none "no crontab"
else
	printf '%s\n' "$CRONTAB" | grep -vE '^[[:space:]]*(#|$|[A-Z_]+[[:space:]]*=)' | while IFS= read -r line; do
		if [[ "$line" == *">/dev/null"*"2>&1"* || "$line" == *"> /dev/null"*"2>&1"* || "$line" == *"&>/dev/null"* || "$line" == *"&> /dev/null"* ]]; then
			verdict="silent: all output discarded"
		elif [[ "$line" == *">/dev/null"* || "$line" == *"> /dev/null"* ]]; then
			verdict="emails only what goes to stderr"
		else
			verdict="may email: whatever it prints is mailed"
		fi
		printf '   %-40s %s\n' "$verdict" "$(printf '%s' "$line" | redact | cut -c1-110)"
	done
fi

# ------------------------------------------------------------------ delivery

section "where mail to the account is forwarded"
found=0
for f in "$HOME/.forward" "$HOME"/.qmail "$HOME"/.qmail-* "$HOME/.procmailrc" "$HOME/.mailfilter"; do
	[ -e "$f" ] || continue
	found=1
	printf '   %s:\n' "$(home "$f")"
	redact < "$f" 2>/dev/null | head -20 | sed 's/^/      /'
	while read -r a; do domain_of "$a"; done < <(grep -Eo '[[:alnum:]._%+-]+@[[:alnum:].-]+' "$f" 2>/dev/null)
done
[ "$found" -eq 1 ] || none "no .forward, .qmail or filter file in the home directory; forwarding, if any, is set in the hosting panel"

# ------------------------------------------------------------------ the mail program

section "the mail program cron hands its mail to"
SENDMAIL="$(command -v sendmail 2>/dev/null || true)"
[ -n "$SENDMAIL" ] || for s in /usr/sbin/sendmail /usr/lib/sendmail; do [ -x "$s" ] && SENDMAIL="$s" && break; done
if [ -z "$SENDMAIL" ]; then
	none "no sendmail found on the PATH or in /usr/sbin"
else
	printf '   sendmail: %s -> %s\n' "$SENDMAIL" "$(readlink -f "$SENDMAIL")"
fi

if command -v exim >/dev/null 2>&1 || [ -x /usr/sbin/exim ]; then
	EXIM="$(command -v exim 2>/dev/null || echo /usr/sbin/exim)"
	printf '   exim:     %s\n' "$("$EXIM" -bV 2>/dev/null | head -1)"
	for opt in primary_hostname qualify_domain; do
		v="$("$EXIM" -bP "$opt" 2>/dev/null)" && printf '   %s\n' "$v"
	done
	# Whether exim signs with DKIM, and as which domain and selector. The
	# private key's location is blanked with the rest by redact.
	dkim="$("$EXIM" -bP transports 2>/dev/null | grep -i dkim | redact)"
	if [ -n "$dkim" ]; then
		printf '   DKIM in its transports:\n'
		printf '%s\n' "$dkim" | sed 's/^/      /'
	else
		printf '   DKIM in its transports: none that this account can see\n'
	fi
fi

if command -v postconf >/dev/null 2>&1; then
	printf '   postfix:  %s\n' "$(postconf -h mail_version 2>/dev/null)"
	for opt in myhostname myorigin mydomain smtpd_milters non_smtpd_milters; do
		printf '   %-18s %s\n' "$opt" "$(postconf -h "$opt" 2>/dev/null | redact)"
	done
	printf '   (non_smtpd_milters is what would sign locally sent mail, such as cron'"'"'s)\n'
fi

if pgrep -x opendkim >/dev/null 2>&1 || [ -e /etc/opendkim.conf ]; then
	printf '   opendkim: present\n'
fi

if [ -n "$SENDMAIL" ] && ! command -v exim >/dev/null 2>&1 && ! command -v postconf >/dev/null 2>&1; then
	printf '   %s\n' "$("$SENDMAIL" -bV 2>&1 | head -1 | redact)"
fi

# ------------------------------------------------------------------ the apps

section "each application's own mail settings"
# The apps are found from the crontab -- every `cd <dir>` in it -- plus this
# one's own directory, so the report covers every app on the account without
# a list here to keep up to date.
APP_DIRS=()
[ -z "$APP_DIR" ] || APP_DIRS+=("$APP_DIR")
while IFS= read -r d; do
	APP_DIRS+=("$d")
done < <(printf '%s\n' "$CRONTAB" | grep -oE 'cd [^ ;&|]+' | cut -c4- | sort -u)

seen=" "
for d in "${APP_DIRS[@]}"; do
	dir="${d/#\~/$HOME}"
	[[ "$dir" == /* ]] || dir="$HOME/$dir"
	dir="$(cd "$dir" 2>/dev/null && pwd)" || continue
	[[ "$seen" == *" $dir "* ]] && continue
	seen="$seen$dir "

	printf '   %s\n' "$(home "$dir")"
	any=0

	# A Go app's config.toml: the [mail] section, by key, never the file.
	if [ -r "$dir/config.toml" ]; then
		awk '
			/^[[:space:]]*\[/ { inmail = ($0 ~ /^[[:space:]]*\[mail\][[:space:]]*$/); next }
			!inmail { next }
			/^[[:space:]]*#/ { next }
			{
				line = $0
				key = line; sub(/[[:space:]]*=.*/, "", key); gsub(/[[:space:]]/, "", key)
				val = line; sub(/^[^=]*=[[:space:]]*/, "", val); sub(/[[:space:]]+#.*$/, "", val)
				if (key == "password") {
					gsub(/"/, "", val)
					print "      config.toml [mail] password is " (val == "" ? "empty" : "set (not shown)")
				} else if (key == "host" || key == "port" || key == "from" || key == "from_name" || key == "user") {
					print "      config.toml [mail] " key ": " val
				}
			}
		' "$dir/config.toml" | redact
		grep -qE '^[[:space:]]*\[mail\]' "$dir/config.toml" && any=1
		while read -r a; do domain_of "$a"; done < <(awk '/^\[/{m=($0~/^\[mail\]/)} m' "$dir/config.toml" | grep -Eo '[[:alnum:]._%+-]+@[[:alnum:].-]+')
	fi

	# A PHP or Node app's .env: the mail keys by name, the password as set
	# or not.
	if [ -r "$dir/.env" ]; then
		while IFS= read -r line; do
			key="${line%%=*}"
			case "$key" in
			MAIL_PASSWORD|SMTP_PASSWORD|SMTP_PASS)
				val="${line#*=}"; val="${val//\"/}"
				printf '      .env %s is %s\n' "$key" "$([ -n "$val" ] && echo 'set (not shown)' || echo 'empty')" ;;
			MAIL_MAILER|MAIL_DRIVER|MAIL_HOST|MAIL_PORT|MAIL_ENCRYPTION|MAIL_USERNAME|MAIL_FROM_ADDRESS|MAIL_FROM_NAME|SMTP_HOST|SMTP_PORT|SMTP_USER|SMTP_FROM)
				printf '      .env %s\n' "$line" | redact
				domain_of "${line#*=}" ;;
			esac
			any=1
		done < <(grep -E '^(MAIL|SMTP)_[A-Z_]+=' "$dir/.env")
	fi

	[ "$any" -eq 1 ] || printf '      (no [mail] in config.toml and no MAIL_ settings in .env)\n'
done

# ------------------------------------------------------------------ for deploy.sh

# The last line is for deploy.sh, which looks each domain up in DNS on the
# person's machine. Public DNS is not a secret, and doing it there keeps this
# script to what only the server can answer.
printf '\nDOMAINS %s\n' "$(printf '%s\n' "${DOMAINS[@]}" | sort -u | tr '\n' ' ')"
