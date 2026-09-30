#!/usr/bin/env bash
#
# The front end is one small file and it is the only thing between a healthy
# binary and a person, so the handful of things it must do are pinned here.
#
# Taken from mass-intentions, where it was written after 2026-09-22: the front
# page was a 404 for everybody while /healthz answered 200 and the deploy
# reported success. Apache mapped "/" onto the docroot, mod_dir substituted a
# DirectoryIndex, and the Go server was asked for a file it has no route for --
# so every path worked except the first one anybody types.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMPLATE="$REPO_DIR/deploy/htaccess.template"

pass=0
fail=0

check() {
	local what="$1"; shift
	if "$@"; then printf '  ok   %s\n' "$what"; pass=$((pass + 1))
	else printf '  FAIL %s\n' "$what"; fail=$((fail + 1)); fi
}

# What deploy.sh writes, so the assertions below are about the real file.
RENDERED="$(sed 's/__APP_PORT__/8451/' "$TEMPLATE")"

has() { printf '%s\n' "$RENDERED" | grep -Eq "$1"; }

# The line number a rule lands on, for the ordering assertions.
where() { printf '%s\n' "$RENDERED" | grep -nE "$1" | head -1 | cut -d: -f1; }

echo "the front end"

check "nothing is left to substitute" bash -c "! printf '%s' \"\$0\" | grep -q '__'" "$RENDERED"

check "mod_dir does not answer for the front page" has '^DirectoryIndex disabled$'
check "and the index it would have asked for is proxied to the front page" \
	has '^RewriteRule \^index\\\.html\$ http://127\.0\.0\.1:8451/ \[P'

check "everything else is proxied to the loopback" \
	has '^RewriteRule \^\(\.\*\)\$ http://127\.0\.0\.1:8451/\$1 \[P'

# The port rendered above is the one the binary listens on by default. If the
# two drift, Apache proxies to a port nothing answers and every page is a 502.
check "the port is the binary's default" \
	grep -q '"127.0.0.1:8451"' "$REPO_DIR/cmd/stewards/config.go"

echo
echo "order, which is the whole of what a rewrite file is"

acme="$(where 'well-known')"
https="$(where 'https://%\{HTTP_HOST\}')"
index="$(where '\^index')"
catchall="$(where '\^\(\.\*\)\$ http://127')"

before() { [ -n "$1" ] && [ -n "$2" ] && [ "$1" -lt "$2" ]; }

# ACME first. A certificate that cannot renew takes the site down months later,
# for a reason nobody connects to this file.
check "the ACME path is exempted before anything else rewrites" before "$acme" "$catchall"

check "the index rule comes before the catch-all that would swallow it" before "$index" "$catchall"

check "http is redirected to https before anything is proxied" before "$https" "$catchall"

# The two checks mass-intentions makes on deploy.sh -- that every deploy
# reinstalls this file, and that the public health check asks for the front
# page and not only /healthz -- come back with deploy.sh. They are the half of
# the 2026-09-22 lesson this file cannot enforce on its own.

echo
printf '%d passed, %d failed\n' "$pass" "$fail"
test "$fail" -eq 0
