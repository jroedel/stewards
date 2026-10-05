#!/usr/bin/env bash
#
# The deploy's moving parts, each tested as shipped.
#
# Every function under test is extracted from deploy/deploy.sh or
# deploy/supervise.sh with sed and eval, rather than reimplemented here, so the
# test is of what deploys and not of a copy that can drift. Nothing reaches the
# server: `remote` and `remote_in_app` run their commands in a temporary
# directory standing in for APP_DIR.
#
# Predicates are called by name and never through `bash -c`. A shell function
# is not exported to a subshell, so `bash -c 'f && exit 1; exit 0'` reports
# "command not found" and passes whatever f does -- mass-intentions' first
# supersede test did exactly that in four of its five assertions.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEPLOY="$REPO_DIR/deploy/deploy.sh"
SUPERVISE="$REPO_DIR/deploy/supervise.sh"

pass=0
fail=0

check() {
	local what="$1"; shift
	if "$@"; then printf '  ok   %s\n' "$what"; pass=$((pass + 1))
	else printf '  FAIL %s\n' "$what"; fail=$((fail + 1)); fi
}

TMP="$(mktemp -d)"
cleanup() {
	# Any process a test started, so a failed assertion cannot leave a sleep
	# running on a CI runner.
	jobs -p | xargs -r kill 2>/dev/null || true
	rm -rf "$TMP"
}
trap cleanup EXIT

extract() { sed -n "/^$2() {/,/^}/p" "$1"; }
body() { extract "$DEPLOY" "$1"; }

log()  { :; }
ok()   { :; }
bad()  { :; }
warn() { :; }

APP=stewards
APP_DIR="$TMP/app"
Q_DIR="$(printf '%q' "$APP_DIR")"
mkdir -p "$APP_DIR"

remote()        { bash -c "$1"; }
remote_in_app() { ( cd "$APP_DIR" && bash -c "$1" ); }

# ------------------------------------------------------------------ rollback

echo "rolling back"

eval "$(body do_rollback)"

# supervise.sh, faked to the one behaviour that matters: it refuses to start a
# binary whose contents say it is bad, which is what the real one does by way
# of the binary exiting and the pid check failing.
cat > "$APP_DIR/supervise.sh" <<'FAKE'
#!/usr/bin/env bash
case "$1" in
start) grep -q '^bad' ./stewards && exit 1; echo running > ./state ;;
stop)  rm -f ./state ;;
esac
exit 0
FAKE
chmod +x "$APP_DIR/supervise.sh"

up() { [ -f "$APP_DIR/state" ]; }
holds() { grep -q "$1" "$APP_DIR/stewards"; }

# A good release, live, and recorded as known good by the deploy that shipped it.
printf 'good v1\n' > "$APP_DIR/stewards"
cp -p "$APP_DIR/stewards" "$APP_DIR/stewards.last-good"

# Two failed deploys in a row. Each copies what is in place into .prev first,
# as cmd_deploy does, so by the second .prev is itself bad -- which is why .prev
# alone is not enough.
for v in 2 3; do
	cp -p "$APP_DIR/stewards" "$APP_DIR/stewards.prev"
	printf 'bad v%s\n' "$v" > "$APP_DIR/stewards"
	do_rollback >/dev/null 2>&1
done

check "two failed deploys in a row still roll back to a binary that works" holds 'good v1'
check "and it is running" up
check "the failed binary is kept for a person to look at" test -f "$APP_DIR/stewards.failed"
check "the known-good copy is kept, not consumed" test -f "$APP_DIR/stewards.last-good"

deploy_body() { body cmd_deploy; }
guarded_start() { deploy_body | grep -q 'if ! remote_in_app "./supervise.sh start"; then'; }
start_rolls_back() { deploy_body | sed -n '/if ! remote_in_app "\.\/supervise.sh start"; then/,/^	fi/p' | grep -qE '^[[:space:]]*do_rollback$'; }
health_rolls_back() { deploy_body | sed -n '/if ! health_loopback; then/,/^	fi/p' | grep -qE '^[[:space:]]*do_rollback$'; }

check "the start is guarded, so set -e cannot end the script before the rollback" guarded_start
check "a start that fails rolls back" start_rolls_back
check "a start that does not answer rolls back" health_rolls_back

rm -f "$APP_DIR"/stewards* "$APP_DIR/state" "$APP_DIR/supervise.sh"

# ------------------------------------------------------------------ cron

echo
echo "the watchdog, on a crontab shared with mass-intentions"

export CRONFILE="$TMP/crontab.txt"
mkdir -p "$TMP/bin"
cat > "$TMP/bin/crontab" <<'FAKE'
#!/usr/bin/env bash
if [ "${1:-}" = "-l" ]; then
	[ -s "$CRONFILE" ] || exit 1
	cat "$CRONFILE"
else
	cat "$1" > "$CRONFILE"
fi
FAKE
chmod +x "$TMP/bin/crontab"
PATH="$TMP/bin:$PATH"

eval "$(grep '^CRON_MARKER=' "$DEPLOY")"
eval "$(body install_cron)"

# What mass-intentions installs, verbatim in shape, and a line of somebody
# else's, which a shared account will have.
cat > "$CRONFILE" <<'CRON'
0 3 * * * /home/someone/backup.sh
@reboot /home/u/intentions/supervise.sh start production # mass-intentions
*/5 * * * * /home/u/intentions/supervise.sh start production # mass-intentions
0 6 * * 0 cd /home/u/intentions && ./intentions -config config.toml -distribute # mass-intentions
CRON

for _ in 1 2 3; do install_cron; done

count() { grep -c -- "$1" "$CRONFILE" || true; }
ours() { grep -c ' # stewards$' "$CRONFILE" || true; }

check "three deploys leave exactly our two lines" test "$(ours)" -eq 2
check "one of them is the watchdog, every five minutes" \
	grep -q "^\*/5 \* \* \* \* cd $Q_DIR && ./supervise.sh watch # stewards$" "$CRONFILE"
check "and the other starts it at boot" \
	grep -q "^@reboot cd $Q_DIR && ./supervise.sh start # stewards$" "$CRONFILE"
check "every mass-intentions line is left alone" test "$(count '# mass-intentions')" -eq 3
check "and so is somebody else's" test "$(count 'backup.sh')" -eq 1

# The other direction. mass-intentions' install_cron removes every line matching
# this before writing its own -- copied here from its deploy.sh, because the
# repository is not available to CI. If ours matched it, its next deploy would
# delete our watchdog and nothing would restart this app again.
MASS_INTENTIONS_STALE='# mass-intentions|supervise\.sh start (production|rehearsal)'
survives_theirs() { [ "$(grep -Ev "$MASS_INTENTIONS_STALE" "$CRONFILE" | grep -c ' # stewards$')" -eq 2 ]; }
check "mass-intentions' next deploy leaves our watchdog in place" survives_theirs

# One of ours at a path we no longer use. Found by the marker wherever it
# points, so a second watchdog cannot run an old binary for ever.
echo "*/5 * * * * cd /old/path && ./supervise.sh start # stewards" >> "$CRONFILE"
install_cron
check "a line of ours at an old path is removed" bash -c '! grep -q /old/path "$0"' "$CRONFILE"

# The watchdog line as deploys before watch wrote it, which is what the
# server has until the first deploy of this one: replaced, not kept beside.
echo "*/5 * * * * cd $Q_DIR && ./supervise.sh start # stewards" >> "$CRONFILE"
install_cron
check "the old, talkative watchdog is replaced" test "$(count 'supervise.sh start # stewards$')" -eq 1

# ------------------------------------------------------------------ supersession

echo
echo "supersession"

eval "$(body superseded)"
fresh() { ! superseded; }

git init -q --bare "$TMP/origin.git"
git clone -q "$TMP/origin.git" "$TMP/work" 2>/dev/null
git -C "$TMP/work" config user.email test@example.invalid
git -C "$TMP/work" config user.name test
git -C "$TMP/work" commit -q --allow-empty -m first
git -C "$TMP/work" branch -M main
git -C "$TMP/work" push -q origin main

SAVED_REPO_DIR="$REPO_DIR"
REPO_DIR="$TMP/work"

check "a checkout at main's tip is not superseded" fresh

git -C "$TMP/work" commit -q --allow-empty -m second
git -C "$TMP/work" push -q origin main
git -C "$TMP/work" reset -q --hard HEAD~1
check "a checkout behind main's tip is superseded" superseded

REPO_DIR="$TMP/no-such-repo"
check "an unanswerable question deploys rather than silently skipping" fresh

REPO_DIR="$TMP/work"
DEPLOY_ALLOW_BRANCH=1
check "a deliberate branch deploy is never superseded" fresh
unset DEPLOY_ALLOW_BRANCH

REPO_DIR="$SAVED_REPO_DIR"

# ------------------------------------------------------------------ supervise

echo
echo "which process is the server"

eval "$(extract "$SUPERVISE" running)"
PIDFILE="$TMP/stewards.pid"
is_running() { running >/dev/null; }
not_running() { ! is_running; }

# The server as run.sh leaves it: argv[0] is ./stewards.
bash -c 'exec -a ./stewards sleep 30' &
printf '%s\n' "$!" > "$PIDFILE"
check "the server is recognised by its argv[0]" is_running

# Everything else in the directory has "stewards" in its path, and none of it is
# the server. A substring match, as in mass-intentions, takes each of these for it.
for impostor in "$APP_DIR/supervise.sh" "/home/u/stewards/run.sh" "vim stewards.log"; do
	bash -c "exec -a '$impostor' sleep 30" &
	printf '%s\n' "$!" > "$PIDFILE"
	check "\"$impostor\" is not taken for the server" not_running
done

printf '999999\n' > "$PIDFILE"
check "a pid that is not alive is not running" not_running

jobs -p | xargs -r kill 2>/dev/null || true

# ------------------------------------------------------------------ the front page

echo
echo "who answered the front page"

eval "$(body answered_by_app)"

app_headers="HTTP/2 404
content-security-policy: default-src 'none'; style-src 'self'
content-type: text/plain; charset=utf-8"

apache_headers="HTTP/2 404
content-type: text/html; charset=iso-8859-1
server: Apache"

app_answered()    { answered_by_app <<<"$app_headers"; }
apache_answered() { answered_by_app <<<"$apache_headers"; }
not_apache()      { ! apache_answered; }

check "the app's own 404 counts as the app answering" app_answered
check "Apache's 404 does not" not_apache

# ------------------------------------------------------------------ backup

echo
echo "backups"

KEEP_BACKUPS=2
eval "$(body backup_script)"

# The stamp comes from `date`, which is replaced so that four backups in the
# same second have four names.
# The count is kept in a file: backup_script is called inside $(...), a
# subshell, and a variable incremented there never reaches the next call.
echo 0 > "$TMP/stamps"
date() { local n; n=$(( $(cat "$TMP/stamps") + 1 )); echo "$n" > "$TMP/stamps"; printf '20260930T00000%dZ' "$n"; }

run_backup() { ( cd "$APP_DIR" && bash -c "$(backup_script)
echo after-the-snippet" ); }

out="$(run_backup)"
said_no_db()    { grep -q 'no database yet' <<<"$out"; }
carried_on()    { grep -q 'after-the-snippet' <<<"$out"; }
check "with no database yet, it says so" said_no_db
check "and does not end the command it is part of" carried_on

printf 'db\n' > "$APP_DIR/stewards.db"
for _ in 1 2 3 4; do run_backup >/dev/null; done

kept() { find "$APP_DIR/backups" -name 'stewards-*.db' | wc -l | tr -d ' '; }
newest_kept() { test -f "$APP_DIR/backups/stewards-20260930T000005Z.db"; }
check "four backups with KEEP_BACKUPS=2 leave two (saw $(kept))" test "$(kept)" -eq 2
check "and the two kept are the newest" newest_kept
check "the backups directory is private" test "$(stat -c %a "$APP_DIR/backups")" = 700

# The photos: snapshotted beside each backup, as links rather than copies.
# Invented files, never a photo (CLAUDE.md).
mkdir -p "$APP_DIR/photo-files/inbox"
printf 'a picture\n' > "$APP_DIR/photo-files/abc-large.jpg"
printf 'an inbox picture\n' > "$APP_DIR/photo-files/inbox/def-small.jpg"

out="$(run_backup 2>&1)"
snap="$(find "$APP_DIR/backups" -maxdepth 1 -name 'photo-files-*' | sort -r | head -1)"

snapshotted()   { test -n "$snap" && test -f "$snap/abc-large.jpg" && test -f "$snap/inbox/def-small.jpg"; }
same_inode()    { test "$(stat -c %i "$snap/abc-large.jpg")" = "$(stat -c %i "$APP_DIR/photo-files/abc-large.jpg")"; }
said_snapshot() { grep -q 'photos snapshotted to backups/photo-files-' <<<"$out"; }
check "the photos are snapshotted, inbox and all" snapshotted
check "as hard links, not copies" same_inode
check "and it says so" said_snapshot

# A photo removed after the backup -- discarded, pruned, or by a bad release
# -- is still in the snapshot.
rm "$APP_DIR/photo-files/abc-large.jpg"
survives() { test "$(cat "$snap/abc-large.jpg")" = 'a picture'; }
check "a photo removed afterwards survives in the snapshot" survives

for _ in 1 2 3; do run_backup >/dev/null 2>&1; done
snaps() { find "$APP_DIR/backups" -maxdepth 1 -name 'photo-files-*' | wc -l | tr -d ' '; }
check "snapshots are kept to KEEP_BACKUPS too (saw $(snaps))" test "$(snaps)" -eq 2

# A host whose cp cannot link: the snapshot warns, the database is backed up
# all the same, and the command the snippet is part of carries on.
mkdir -p "$TMP/nocp"
printf '#!/bin/sh\nfor a in "$@"; do [ "$a" = -al ] && { echo "cp: cannot create hard link" >&2; exit 1; }; done\nexec /bin/cp "$@"\n' > "$TMP/nocp/cp"
chmod +x "$TMP/nocp/cp"

before="$(kept)"
out="$( ( cd "$APP_DIR" && PATH="$TMP/nocp:$PATH" bash -c "$(backup_script)
echo after-the-snippet" ) 2>&1 )"

warned()       { grep -q 'WARNING: the photos could not be snapshotted' <<<"$out"; }
db_backed_up() { grep -q 'backed up to backups/stewards-' <<<"$out"; }
no_half_snap() { test "$(snaps)" -le 2 && ! find "$APP_DIR/backups" -maxdepth 1 -name "photo-files-$(cat "$TMP/stamps" | xargs printf '20260930T00000%dZ')" | grep -q .; }
check "a snapshot that fails warns" warned
check "and the database is backed up all the same" db_backed_up
check "and nothing half-made is left" no_half_snap
check "and the command carries on" grep -q 'after-the-snippet' <<<"$out"
unset -f date

# ------------------------------------------------------------------ the lock

echo
echo "the swap holds the supervisor's lock"

eval "$(body locked)"

swap_is_locked() { deploy_body | grep -q 'locked "./supervise.sh stop$'; }
swap_backs_up() { deploy_body | sed -n '/locked "\.\/supervise.sh stop$/,/die/p' | grep -q 'backup_script'; }
swap_renames()  { deploy_body | sed -n '/locked "\.\/supervise.sh stop$/,/die/p' | grep -q "^mv \$APP.new \$APP"; }

check "stop, backup and rename are one locked command" swap_is_locked
check "the backup is inside it" swap_backs_up
check "and so is the rename" swap_renames

# While a locked command runs, a watchdog start -- which takes the same lock --
# must wait rather than start anything.
locked "sleep 2" &
sleep 0.5
watchdog_waits() { ! ( cd "$APP_DIR" && flock -n stewards.lock true ); }
check "a watchdog start during the swap cannot take the lock" watchdog_waits
wait

watchdog_free() { ( cd "$APP_DIR" && flock -n stewards.lock true ); }
check "and can once the swap is over" watchdog_free

# ------------------------------------------------------------------ uploads

echo
echo "the upload check"

eval "$(body cmd_upload_check)"
eval "$(body app_logged)"
eval "$(body upload_verdict)"
eval "$(body answered_by_app)"

# Apache and the app, faked as one curl. FAKE_LIMIT is Apache's upload limit in
# bytes: past it, Apache's own 413, and the app never hears of it. Under it the
# app logs the post and refuses it, signed out; FAKE_APP says how that reaches
# curl: "answer" is the app's 403, "close" is Apache's 502 after the app hung
# up mid-upload, and "down" is a 503 the app never logged.
mkdir -p "$TMP/upcurl"
cat > "$TMP/upcurl/curl" <<'FAKE'
#!/usr/bin/env bash
headers= file= url= expect=sent
while [ $# -gt 0 ]; do
	case "$1" in
	-D) headers="$2"; shift ;;
	--upload-file) file="$2"; shift ;;
	-H) [ "$2" = "Expect:" ] && expect=empty; shift ;;
	https://*) url="$1" ;;
	esac
	shift
done
echo "$expect" >> "$FAKE_EXPECTS"
size="$(stat -c %s "$file")"
path="/${url#https://*/}"
if [ "$size" -gt "$FAKE_LIMIT" ]; then
	printf 'HTTP/1.1 413 Request Entity Too Large\r\nServer: Apache\r\n\r\n' > "$headers"; printf 413; exit 0
fi
case "$FAKE_APP" in
down)   printf 'HTTP/1.1 503 Service Unavailable\r\n\r\n' > "$headers"; printf 503; exit 0 ;;
esac
echo "msg=request method=POST path=$path status=403 bytes=50" >> "$FAKE_LOG"
case "$FAKE_APP" in
answer) printf "HTTP/1.1 403 Forbidden\r\nContent-Security-Policy: default-src 'none'; script-src 'self'\r\n\r\n" > "$headers"; printf 403 ;;
close)  printf 'HTTP/1.1 502 Proxy Error\r\n\r\n' > "$headers"; printf 502 ;;
esac
FAKE
chmod +x "$TMP/upcurl/curl"

export FAKE_LOG="$APP_DIR/stewards.log" FAKE_EXPECTS="$TMP/expects"
APP_HOST=stewards.example.invalid

upload_check() {
	export FAKE_LIMIT="$1" FAKE_APP="$2"
	: > "$FAKE_LOG"
	(
		load_config() { :; }; need_app_dir() { :; }; ssh_setup() { :; }; require() { :; }
		health_public() { :; }
		ok()  { echo "ok: $*"; }
		bad() { echo "bad: $*"; }
		PATH="$TMP/upcurl:$PATH"
		cmd_upload_check
	) 2>&1
}

GB=1073741824

out="$(upload_check $GB answer)"; status=$?
check "under the host's limit, both sizes pass" test "$(grep -c '^ok: Apache passed it' <<<"$out")" -eq 2
check "and the check succeeds" test "$status" -eq 0

out="$(upload_check $GB close)"; status=$?
check "a 502 for an upload the app logged is still a pass" test "$(grep -c '^ok: Apache passed it' <<<"$out")" -eq 2
check "and the check succeeds" test "$status" -eq 0

set +e
out="$(upload_check $((100 << 20)) close)"; status=$?
set -e
photo_passes()   { grep -q '^ok: Apache passed it' <<<"$out"; }
batch_refused()  { grep -q '^bad: Apache refused it with 413: the host.s upload limit is below 160 MB' <<<"$out"; }
check "a 100 MB limit passes one photo" photo_passes
check "and refuses the batch, naming its size" batch_refused
check "and the check fails" test "$status" -ne 0

set +e
out="$(upload_check $GB down)"; status=$?
set -e
check "an answer the app never logged is not called a pass" grep -q '^bad: Apache answered 503, and the app did not log it' <<<"$out"
check "and the check fails" test "$status" -ne 0

check "every probe sends an empty Expect, so Apache checks the size first" test "$(sort -u "$FAKE_EXPECTS")" = empty
rm -f "$FAKE_LOG"

echo
printf '%d passed, %d failed\n' "$pass" "$fail"
test "$fail" -eq 0
