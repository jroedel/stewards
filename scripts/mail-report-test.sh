#!/usr/bin/env bash
#
# deploy/mail-report.sh, run against a made-up account full of planted
# secrets, must print the settings and none of the secrets.
#
# The account is a temporary HOME with a fake crontab on the PATH. Every
# secret below is invented and starts with FAKE, so one grep finds any that
# got through. Nothing here reaches the server.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPORT="$REPO_DIR/deploy/mail-report.sh"

pass=0
fail=0

check() {
	local what="$1"; shift
	if "$@"; then printf '  ok   %s\n' "$what"; pass=$((pass + 1))
	else printf '  FAIL %s\n' "$what"; fail=$((fail + 1)); fi
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

export HOME="$TMP/home"
mkdir -p "$HOME/stewards" "$HOME/public_html/other-app" "$TMP/bin"

# The crontab as a shared account might have it: our watchdog, a job that
# discards its output, and three ways people paste credentials into cron.
cat > "$TMP/crontab.txt" <<'CRON'
MAILTO=steward@example.org
@reboot cd stewards && ./supervise.sh start # stewards
*/5 * * * * cd stewards && ./supervise.sh start # stewards
0 3 * * * cd public_html/other-app && php artisan schedule:run >/dev/null 2>&1
0 4 * * * curl -s "https://api.example.invalid/ping?token=FAKEtoken1" >/dev/null
0 5 * * * mysqldump -uapp -pFAKEdbpass app > /dev/null
0 6 * * * curl -s https://app:FAKEurlpass@backup.example.invalid/ >/dev/null
0 7 * * * API_KEY=FAKEapikey2 ./sync.sh
0 8 * * * ./job --secret FAKEsecretflag3 --id FAKEabcdefghijklmnopqrstuvwxyz
CRON
cat > "$TMP/bin/crontab" <<FAKE
#!/usr/bin/env bash
[ "\${1:-}" = "-l" ] && cat "$TMP/crontab.txt"
FAKE
chmod +x "$TMP/bin/crontab"
PATH="$TMP/bin:$PATH"

cat > "$HOME/stewards/config.toml" <<'TOML'
[server]
addr = "127.0.0.1:8451"
[mail]
host = "relay.example.invalid"
port = 587
user = "stewards@example.org"
password = "FAKEmailpass"
from = "stewards@example.org"
from_name = "Garden stewards"
[auth]
bootstrap_secret = "FAKEbootstrapsecretFAKEbootstrapsecret"
TOML

cat > "$HOME/public_html/other-app/.env" <<'ENV'
APP_KEY=base64:FAKEappkey
DB_PASSWORD=FAKEenvdbpass
MAIL_MAILER=smtp
MAIL_HOST=relay.example.invalid
MAIL_USERNAME=other@example.net
MAIL_PASSWORD=FAKEenvmailpass
MAIL_FROM_ADDRESS="other@example.net"
ENV

printf 'person@example.com\n' > "$HOME/.forward"

out="$(bash "$REPORT" stewards 2>&1)"
has() { grep -qF -- "$1" <<<"$out"; }
lacks() { ! grep -qF -- "$1" <<<"$out"; }

echo "what it says"
check "cron's MAILTO" has "MAILTO:   steward@example.org"
check "that MAILFROM is not set" has "MAILFROM: not set"
check "that the five-minute watchdog emails on every run" \
	grep -qE 'may email: .*\*/5 .*supervise\.sh start' <<<"$out"
check "that a job discarding its output is silent" \
	grep -qE 'silent: all output discarded +0 3 ' <<<"$out"
check "the forward" has "person@example.com"
check "this app's relay, by key" has 'config.toml [mail] host: "relay.example.invalid"'
check "and its From" has 'config.toml [mail] from: "stewards@example.org"'
check "that its password is set, and no more" has "config.toml [mail] password is set (not shown)"
check "the other app, found from the crontab" has "~/public_html/other-app"
check "its mail host" has ".env MAIL_HOST=relay.example.invalid"
check "that its password is set" has ".env MAIL_PASSWORD is set (not shown)"
check "every domain it met, for DNS" \
	grep -qE '^DOMAINS .*example\.com .*example\.net .*example\.org' <<<"$out"

echo
echo "what it never says"
check "no planted secret, anywhere" lacks "FAKE"
check "not the bootstrap secret's key either" lacks "bootstrap_secret"
check "nor any .env key that is not about mail" lacks "DB_PASSWORD"

echo
echo "how deploy.sh runs it"
# Piped to bash -s, so nothing is installed on the server, and through
# remote rather than anything that writes there.
mail_body="$(sed -n '/^cmd_mail_report() {/,/^}/p' "$REPO_DIR/deploy/deploy.sh")"
check "it is piped over ssh, not copied there" \
	grep -qF 'remote "bash -s -- $Q_DIR" < "$REPO_DIR/deploy/mail-report.sh"' <<<"$mail_body"
check "and nothing is written to the server" \
	bash -c '! grep -qE "push_file|locked|remote_in_app|scp" <<<"$0"' "$mail_body"

echo
echo "what DNS it looks up, on this machine"
# dig, faked to a zone where the host's domain has SPF and DMARC and one
# domain has a DKIM key under a konsoleH-style selector.
# Like the real one, it takes several NAME TYPE pairs in one call, and with
# +answer names each record it prints; +short's single query is TYPE NAME.
cat > "$TMP/bin/dig" <<'FAKE'
#!/usr/bin/env bash
short=0; [[ " $* " == *" +short "* ]] && short=1
args=(); for a in "$@"; do [[ "$a" == +* ]] || args+=("$a"); done
for ((i = 0; i + 1 < ${#args[@]}; i += 2)); do
	if [ "$short" -eq 1 ]; then type="${args[i]}" name="${args[i+1]}"; else name="${args[i]}" type="${args[i+1]}"; fi
	case "$type $name" in
	"TXT example.org")                        v='"v=spf1 +a +mx ?all"' ;;
	"TXT _dmarc.example.org")                 v='"v=DMARC1; p=quarantine"' ;;
	"TXT default2503._domainkey.example.org") v='"v=DKIM1; k=rsa; p=MIIB"' ;;
	"A example.org"|"A host.example.org")     v=192.0.2.10 ;;
	*) continue ;;
	esac
	if [ "$short" -eq 1 ]; then echo "$v"; else printf '%s.\t300\tIN\t%s\t%s\n' "$name" "$type" "$v"; fi
done
FAKE
chmod +x "$TMP/bin/dig"
eval "$(sed -n '/^dns_report() {/,/^}/p' "$REPO_DIR/deploy/deploy.sh")"
dns="$(dns_report host.example.org example.net)"
dns_has() { grep -qF -- "$1" <<<"$dns"; }
check "a host name is looked up with its domain" dns_has "   example.org"
check "the domain's SPF" dns_has 'SPF:   "v=spf1 +a +mx ?all"'
check "its DMARC" dns_has 'DMARC: "v=DMARC1; p=quarantine"'
check "a DKIM key under a dated selector, named once" \
	grep -qxF '      DKIM:  a key under: default2503' <<<"$dns"
check "a domain with none says so" dns_has "DKIM:  no key under any common selector"
sed -i 's/^\t\*) continue ;;$/\t"TXT custom1._domainkey.example.net") v=p=MIIB ;;\n\t*) continue ;;/' "$TMP/bin/dig"
check "SELECTOR= is tried as well" \
	grep -qF 'DKIM:  a key under: custom1' <<<"$(SELECTOR=custom1 dns_report example.net)"

if [ "$fail" -gt 0 ]; then
	echo
	echo "the report was:"
	printf '%s\n' "$out" | sed 's/^/  | /'
fi

echo
printf '%d passed, %d failed\n' "$pass" "$fail"
test "$fail" -eq 0
