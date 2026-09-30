#!/usr/bin/env bash
#
# A whole deploy, end to end, against a fake server on this machine.
#
# deploy-test.sh tests the parts. This runs the real `deploy/deploy.sh deploy`
# -- the real build, run.sh, supervise.sh and binary -- with ssh, scp and curl
# replaced on PATH:
#
#   ssh   runs the command in a temporary directory standing in for the
#         account's home, the way konsoleH runs it in ours;
#   scp   copies into that directory;
#   curl  sends https://<APP_HOST>/... to the app on the loopback, and answers
#         503 when nothing is listening, which is what Apache's proxy does.
#
# So what is tested is everything but the network: three deploys in a row --
# the first onto an empty account, a second over it, and a third shipping a
# binary that passes its config check and then cannot start, which must be
# rolled back to the second.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

pass=0
fail=0

check() {
	local what="$1"; shift
	if "$@"; then printf '  ok   %s\n' "$what"; pass=$((pass + 1))
	else printf '  FAIL %s\n' "$what"; fail=$((fail + 1)); fi
}

TMP="$(mktemp -d)"
HOME_DIR="$TMP/home"
PORT=18453

cleanup() {
	# The server the deploy started, whatever state the test ended in.
	[ -x "$HOME_DIR/stewards/supervise.sh" ] && "$HOME_DIR/stewards/supervise.sh" stop >/dev/null 2>&1 || true
	rm -rf "$TMP"
}
trap cleanup EXIT

mkdir -p "$HOME_DIR/public_html/stewards.example.invalid/public" "$TMP/bin"

# The fakes. REAL_CURL is resolved before PATH changes, so the fake can call it.
REAL_CURL="$(command -v curl)"

cat > "$TMP/bin/ssh" <<FAKE
#!/usr/bin/env bash
# The command is the last argument; everything before it is options and the
# destination, which a fake server has no use for.
cd "$HOME_DIR" && exec bash -c "\${@: -1}"
FAKE

cat > "$TMP/bin/scp" <<FAKE
#!/usr/bin/env bash
src="\${@: -2:1}"; dest="\${@: -1}"
cp "\$src" "$HOME_DIR/\${dest#*:}"
FAKE

# crontab, kept in a file, because this machine's own must not be touched.
cat > "$TMP/bin/crontab" <<FAKE
#!/usr/bin/env bash
if [ "\${1:-}" = "-l" ]; then cat "$TMP/crontab.txt" 2>/dev/null; else cat "\$1" > "$TMP/crontab.txt"; fi
FAKE

cat > "$TMP/bin/curl" <<FAKE
#!/usr/bin/env bash
args=()
for a in "\$@"; do args+=("\${a/https:\/\/stewards.example.invalid/http://127.0.0.1:$PORT}"); done

if "$REAL_CURL" "\${args[@]}"; then exit 0; else code=\$?; fi

# Connection refused: nothing on the port. Answer as Apache's proxy would,
# in whichever form the caller asked for.
if [ "\$code" -eq 7 ]; then
	case " \$* " in
	*" -D - "*) printf 'HTTP/1.1 503 Service Unavailable\r\nserver: Apache\r\n\r\n' ;;
	*"%{http_code}"*) printf '503' ;;
	esac
	exit 0
fi
exit "\$code"
FAKE
chmod +x "$TMP/bin/"*

# The key only has to be one ssh would accept; the fake never uses it.
ssh-keygen -q -t ed25519 -f "$TMP/key" -N ""

export PATH="$TMP/bin:$PATH"
export SECRETS_ENV="$TMP/no-secrets.env"   # never the real one; absent on purpose
export DEPLOY_SSH_HOST=example.invalid DEPLOY_SSH_USER=u DEPLOY_SSH_PORT=22
export DEPLOY_SSH_KEY_B64; DEPLOY_SSH_KEY_B64="$(base64 -w0 < "$TMP/key")"
export DEPLOY_KNOWN_HOSTS_B64; DEPLOY_KNOWN_HOSTS_B64="$(printf 'example.invalid ssh-ed25519 AAAA\n' | base64 -w0)"
export APP_HOST=stewards.example.invalid APP_PORT=$PORT APP_DIR=stewards
export APP_DOCROOT=public_html/stewards.example.invalid/public KEEP_BACKUPS=2

# A branch, and not main's tip, so the git guards are told this is deliberate.
export DEPLOY_ALLOW_BRANCH=1

APPDIR="$HOME_DIR/stewards"
deploy() { "$REPO_DIR/deploy/deploy.sh" deploy > "$TMP/out" 2>&1; }

# A deploy expected to succeed shows its output when it does not, so a red CI
# run says why without anybody re-running it by hand.
deploys() { deploy || { sed 's/^/     | /' "$TMP/out"; return 1; }; }
said() { grep -q -- "$1" "$TMP/out"; }
healthy() { "$REAL_CURL" -sf "http://127.0.0.1:$PORT/healthz" >/dev/null; }

echo "a first deploy, with no config.toml on the server"
deploy_refused() { ! deploy; }
check "is refused" deploy_refused
check "and says what to run" said "make deploy-send-secrets"
check "and nothing was swapped in" test ! -e "$APPDIR/stewards"

# What make deploy-send-secrets writes, rendered by the real script.
mkdir -p "$APPDIR"
printf '[server]\naddr = "127.0.0.1:%s"\nshutdown_grace = "2s"\n[db]\npath = "stewards.db"\n' "$PORT" > "$APPDIR/config.toml"

echo
echo "a first deploy, onto an empty account"
check "succeeds" deploys
check "and the app answers" healthy
check "the front end is installed" test -f "$HOME_DIR/$APP_DOCROOT/.htaccess"
check "the watchdog is scheduled" grep -q 'supervise.sh start # stewards$' "$TMP/crontab.txt"
check "the binary is recorded as known good" test -f "$APPDIR/stewards.last-good"
check "the public checks passed" said "answered by the app"

echo
echo "a second deploy, over a running app"
check "succeeds" deploys
check "the app answers" healthy
check "the database was backed up first" bash -c 'ls "$0"/backups/stewards-*.db >/dev/null 2>&1' "$APPDIR"
check "the binary before it is kept as .prev" test -f "$APPDIR/stewards.prev"

echo
echo "a binary that passes -check and cannot start"
# Swapped in for the release build: it answers -check exactly as the real one
# would, so it clears the pre-flight, and then exits at once.
good_summary="$("$APPDIR/stewards" -config "$APPDIR/config.toml" -check)"
mkdir -p "$TMP/badmake"
cat > "$TMP/badmake/make" <<FAKE
#!/usr/bin/env bash
cat > "$REPO_DIR/stewards-linux-amd64" <<'BAD'
#!/usr/bin/env bash
case " \$* " in *" -check "*) cat <<'SUMMARY'
$good_summary
SUMMARY
exit 0 ;; esac
echo "this binary is broken on purpose" >&2
exit 1
BAD
chmod +x "$REPO_DIR/stewards-linux-amd64"
FAKE
chmod +x "$TMP/badmake/make"

bad_deploy() { PATH="$TMP/badmake:$PATH" deploy; }
bad_refused() { ! bad_deploy; }
check "fails the deploy" bad_refused
check "says it rolled back" said "rolled back to the last binary that was known good"
check "and the app answers again, on the good binary" healthy
check "the broken one is kept for a person to look at" test -f "$APPDIR/stewards.failed"
rm -f "$REPO_DIR/stewards-linux-amd64"

echo
printf '%d passed, %d failed\n' "$pass" "$fail"
test "$fail" -eq 0
