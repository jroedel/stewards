#!/usr/bin/env bash
#
# Exercises scripts/stewards-api against a fake curl, so nothing reaches the
# server. The fake records its arguments and the headers it was handed, which
# is how the one property that matters is checked: the key is in the header,
# and never on the command line.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API="$REPO_DIR/scripts/stewards-api"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0
fail=0

check() {
	local what="$1"; shift
	if "$@"; then
		printf '  ok   %s\n' "$what"; pass=$((pass + 1))
	else
		printf '  FAIL %s\n' "$what"; fail=$((fail + 1))
	fi
}

# The fake: every argument on its own line, and the contents of any
# "--header @file" it was given, which is where the key should be.
mkdir -p "$TMP/bin"
cat > "$TMP/bin/curl" <<'FAKE'
#!/usr/bin/env bash
: > "$FAKE_LOG.args"
: > "$FAKE_LOG.headers"
prev=""
for a in "$@"; do
	printf '%s\n' "$a" >> "$FAKE_LOG.args"
	if [ "$prev" = --header ] && [ "${a#@}" != "$a" ]; then
		cat "${a#@}" >> "$FAKE_LOG.headers"
	fi
	prev="$a"
done
echo '{"ok": true}'
FAKE
chmod +x "$TMP/bin/curl"

export PATH="$TMP/bin:$PATH"
export FAKE_LOG="$TMP/curl"
KEY='stw_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.SECRETSECRETSECRETSECRET'

echo "the API helper"

check "the index needs no key" \
	env -u STEWARDS_API_KEY "$API" GET /api/v1
check "and asks for the live site by default" \
	grep -qx 'https://stewards.schoenstatt-fathers.us/api/v1' "$FAKE_LOG.args"

check "anything else without a key is refused" \
	bash -c '! env -u STEWARDS_API_KEY "$0" GET /api/v1/species 2>/dev/null' "$API"
check "and says where a key is made" \
	bash -c 'env -u STEWARDS_API_KEY "$0" GET /api/v1/species 2>&1 | grep -q "/steward/keys"' "$API"

STEWARDS_API_KEY="$KEY" "$API" PUT /api/v1/species/winecup --json '{"status":"native"}' > /dev/null

check "the key goes in the Authorization header" \
	grep -qx "Authorization: Bearer $KEY" "$FAKE_LOG.headers"
check "and never on curl's command line" \
	bash -c '! grep -q SECRETSECRET "$0"' "$FAKE_LOG.args"
check "the caller's options reach curl" \
	grep -qx -- '--json' "$FAKE_LOG.args"
check "and the method" \
	grep -qx PUT "$FAKE_LOG.args"
check "errors fail the command" \
	grep -qx -- '--fail-with-body' "$FAKE_LOG.args"

check "a path outside the API is refused" \
	bash -c '! STEWARDS_API_KEY=x "$0" GET /steward/species 2>/dev/null' "$API"
check "and so is a method it does not take" \
	bash -c '! STEWARDS_API_KEY=x "$0" DELETE /api/v1/species/winecup 2>/dev/null' "$API"

STEWARDS_URL=http://127.0.0.1:18471/ STEWARDS_API_KEY="$KEY" "$API" GET /api/v1/species > /dev/null
check "STEWARDS_URL points it elsewhere" \
	grep -qx 'http://127.0.0.1:18471/api/v1/species' "$FAKE_LOG.args"

echo
printf '%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
