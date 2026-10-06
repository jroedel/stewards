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

# Never the real key file, whoever runs this.
export STEWARDS_API_KEY_FILE="$TMP/no-such-key-file"
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
	bash -c '! STEWARDS_API_KEY=x "$0" OPTIONS /api/v1/species/winecup 2>/dev/null' "$API"

STEWARDS_URL=http://127.0.0.1:18471/ STEWARDS_API_KEY="$KEY" "$API" GET /api/v1/species > /dev/null
check "STEWARDS_URL points it elsewhere" \
	grep -qx 'http://127.0.0.1:18471/api/v1/species' "$FAKE_LOG.args"

# The key file: read when the variable is not set, refused when others can
# read it, and the variable wins when both are there.
FILE="$TMP/api-key"
printf '%s\n' "$KEY" > "$FILE"
chmod 600 "$FILE"

env -u STEWARDS_API_KEY STEWARDS_API_KEY_FILE="$FILE" "$API" GET /api/v1/species > /dev/null
check "the key file is read when the variable is not set" \
	grep -qx "Authorization: Bearer $KEY" "$FAKE_LOG.headers"
check "and its key stays off the command line" \
	bash -c '! grep -q SECRETSECRET "$0"' "$FAKE_LOG.args"

STEWARDS_API_KEY=stw_from.the-variable-not-the-file STEWARDS_API_KEY_FILE="$FILE" "$API" GET /api/v1/species > /dev/null
check "the variable wins over the file" \
	grep -qx "Authorization: Bearer stw_from.the-variable-not-the-file" "$FAKE_LOG.headers"

chmod 644 "$FILE"
check "a key file others can read is refused" \
	bash -c '! env -u STEWARDS_API_KEY STEWARDS_API_KEY_FILE="$1" "$0" GET /api/v1/species 2>/dev/null' "$API" "$FILE"

check "no variable and no file is refused" \
	bash -c '! env -u STEWARDS_API_KEY STEWARDS_API_KEY_FILE=/nonexistent "$0" GET /api/v1/species 2>/dev/null' "$API"

echo
printf '%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
