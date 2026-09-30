#!/usr/bin/env bash
#
# Put the steward app on the konsoleH account.
#
# The target is shared hosting with no root, no systemd and no Docker: ssh,
# cron and a static binary are what everything here is built out of. This is
# the first piece of it, and it does one thing -- the Apache front end. Shipping
# the binary (the swap, the supervisor, cron, backups and rollback) arrives as
# `deploy`, adapted from mass-intentions, in a change of its own.
#
# Run from CI on every push to main, and by a person as `make deploy-htaccess`.
# Never by an agent (CLAUDE.md section 6; .claude/settings.json denies it).
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SECRETS_ENV="${SECRETS_ENV:-$REPO_DIR/secrets.env}"

log()  { printf '\033[1m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33m !!\033[0m %s\n' "$*" >&2; }
ok()   { printf '\033[32m  ok\033[0m %s\n' "$*"; }
die()  { printf '\033[31mX\033[0m %s\n' "$*" >&2; exit 1; }

require() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but is not installed"; }

# shellcheck source=../scripts/env.sh
. "$REPO_DIR/scripts/env.sh"

# ------------------------------------------------------------------ config

# In CI the values arrive as environment variables; on a person's machine they
# come from secrets.env. Same names either way. The file is read only when it
# exists, and a runner never has one.
load_config() {
	[ ! -f "$SECRETS_ENV" ] || parse_env "$SECRETS_ENV"

	: "${DEPLOY_SSH_HOST:?DEPLOY_SSH_HOST is not set. On your machine it belongs in secrets.env; in CI it is a repository secret}"
	: "${DEPLOY_SSH_USER:?DEPLOY_SSH_USER is not set}"
	: "${APP_HOST:?APP_HOST is not set}"
	: "${APP_PORT:?APP_PORT is not set}"
	: "${APP_DOCROOT:?APP_DOCROOT is not set}"

	DEPLOY_SSH_PORT="${DEPLOY_SSH_PORT:-22}"
}

# ssh_setup decodes the key and the pinned host key into a directory removed
# on exit. There is no unpinned fallback: a deploy that trusts whatever answers
# on port 22 is a deploy that can be handed somebody else's shell.
SSH_TMP="$(mktemp -d)"
chmod 700 "$SSH_TMP"
trap 'rm -rf "$SSH_TMP"' EXIT
SSH_OPTS=()

ssh_setup() {
	require ssh

	printf '%s' "${DEPLOY_SSH_KEY_B64:?DEPLOY_SSH_KEY_B64 is not set. Run: make deploy-keygen}" \
		| base64 -d > "$SSH_TMP/key" 2>/dev/null \
		|| die "DEPLOY_SSH_KEY_B64 is not valid base64. Run: make deploy-status"
	chmod 600 "$SSH_TMP/key"

	grep -q 'BEGIN OPENSSH PRIVATE KEY' "$SSH_TMP/key" \
		|| die "DEPLOY_SSH_KEY_B64 decodes to something that is not an ssh private key"

	printf '%s' "${DEPLOY_KNOWN_HOSTS_B64:?DEPLOY_KNOWN_HOSTS_B64 is not set; a deploy must be pinned. Run: make deploy-known-hosts}" \
		| base64 -d > "$SSH_TMP/known_hosts" 2>/dev/null \
		|| die "DEPLOY_KNOWN_HOSTS_B64 is not valid base64. Run: make deploy-known-hosts"

	SSH_OPTS=(
		-i "$SSH_TMP/key"
		-o "UserKnownHostsFile=$SSH_TMP/known_hosts"
		-o StrictHostKeyChecking=yes
		-o ConnectTimeout=15
		-o BatchMode=yes
	)
}

remote() { ssh "${SSH_OPTS[@]}" -p "$DEPLOY_SSH_PORT" "$DEPLOY_SSH_USER@$DEPLOY_SSH_HOST" "$@"; }

# ------------------------------------------------------------------ front end

# render_htaccess is the file exactly as it will be installed.
render_htaccess() {
	[[ "$APP_PORT" =~ ^[0-9]+$ ]] || die "APP_PORT is \"$APP_PORT\"; it must be a port number"
	sed "s/__APP_PORT__/$APP_PORT/" "$REPO_DIR/deploy/htaccess.template"
}

# cmd_htaccess installs the front end and then proves, from outside, that
# Apache is reading it.
#
# The proof is the point. Writing a file over ssh succeeds whether or not it is
# in the directory the vhost serves, and "installed" in a log is exactly what
# a docroot mismatch looks like from the inside. So the job only passes when
# the public address behaves as this file says it should.
cmd_htaccess() {
	load_config
	ssh_setup

	local docroot; docroot="$(printf '%q' "$APP_DOCROOT")"

	log "installing $APP_DOCROOT/.htaccess on $DEPLOY_SSH_HOST, proxying to 127.0.0.1:$APP_PORT"

	# The docroot is not created here. konsoleH creates it when the subdomain
	# is added, and making it ourselves would turn "the subdomain points
	# somewhere else" into a file written to a directory nothing serves -- the
	# very mistake the check below exists to catch, made silently.
	#
	# Through a .new file and a rename, so Apache never reads half a file.
	render_htaccess | remote "
		[ -d $docroot ] || { echo 'no such docroot' >&2; exit 3; }
		cat > $docroot/.htaccess.new && chmod 644 $docroot/.htaccess.new && mv $docroot/.htaccess.new $docroot/.htaccess
	" || die "could not install into $APP_DOCROOT. If it said 'no such docroot', add the subdomain in konsoleH with that docroot, or correct APP_DOCROOT"

	ok "written"

	# A docroot should hold .htaccess and nothing else of ours. Reported, not
	# refused: konsoleH may put its own placeholder there, and deleting files
	# on the server is not this command's business.
	local stray
	stray="$(remote "ls -A $docroot" | grep -vx '.htaccess' || true)"
	[ -z "$stray" ] || warn "the docroot also holds: $(tr '\n' ' ' <<<"$stray")-- only .htaccess belongs there"

	verify_public
}

# verify_public asks the internet what a visitor gets.
#
#   http://  must redirect to https://, which only our rules do.
#   /healthz must be the app (200) or the proxy with nothing behind it yet
#            (502 or 503). Apache's own 404 or 403 means it is serving the
#            directory as files and has not read our rules at all.
verify_public() {
	require curl

	local http code
	http="$(curl -sS -o /dev/null -w '%{http_code} %{redirect_url}' --max-time 15 "http://$APP_HOST/" || true)"

	case "$http" in
	"301 https://$APP_HOST/"*) ok "http redirects to https" ;;
	*) die "http://$APP_HOST/ answered \"$http\", not a redirect to https. Apache is not reading $APP_DOCROOT/.htaccess: check that the subdomain's docroot in konsoleH is exactly $APP_DOCROOT" ;;
	esac

	code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 15 "https://$APP_HOST/healthz" || true)"

	case "$code" in
	200)     ok "https://$APP_HOST/healthz answers 200: the app is live behind the proxy" ;;
	502|503) ok "https://$APP_HOST/ proxies to 127.0.0.1:$APP_PORT; nothing answers there yet ($code), which is right until the binary is deployed" ;;
	*)       die "https://$APP_HOST/healthz answered $code. Expected 200 from the app, or 502/503 from the proxy with nothing behind it" ;;
	esac
}

# ------------------------------------------------------------------ dispatch

usage() {
	cat <<-USAGE
		usage: deploy/deploy.sh <command>

		  htaccess         install the Apache front end, then check it from outside
		  render-htaccess  print the .htaccess that would be installed, touch nothing
	USAGE
}

case "${1:-}" in
htaccess)        shift; cmd_htaccess "$@" ;;
render-htaccess) shift; load_config; render_htaccess ;;
*)               usage; exit 2 ;;
esac
