package userbus

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	"github.com/jroedel/stewards/business/types"
)

// A credential in this package is always two parts, an identifier and a
// secret, handed out together as "<id>.<secret>". Taken from mass-intentions,
// where the reasoning is written at length; the short form:
//
// The identifier is what gets looked up -- indexed, safe to log, one read. The
// secret is never stored, only its hash, and it is compared in constant time.
// Without the split, finding a credential would mean either storing the secret
// searchably or hashing against every live row, which is slow and says how
// many rows there are. And a leaked log line holding only the identifier
// authorises nothing.

// errMalformed is a presented credential that is not even the right shape.
// Never shown to anybody as different from a wrong secret; see ErrDenied.
var errMalformed = errors.New("not a credential")

// hashSecret is plain SHA-256 rather than a password KDF. The secrets here come
// from crypto/rand with at least 128 bits in them, so there is nothing for an
// iteration count to protect. If a secret a person chose ever arrives -- a
// PIN, a password -- it must not use this.
func hashSecret(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))

	return sum[:]
}

// verifySecret compares in constant time. bytes.Equal stops at the first
// difference, and the time that takes is how a hash is recovered one byte at a
// time.
func verifySecret(stored []byte, presented string) bool {
	return subtle.ConstantTimeCompare(stored, hashSecret(presented)) == 1
}

// credential is an identifier and a secret, ready to hand out once. Nothing
// stores this type; the secret exists for one request.
type credential struct {
	id     types.ID
	secret string
	hash   []byte
}

// mintCredential uses rand.Text: at least 128 bits, in the base32 alphabet, so
// it is safe in a URL, in a mail client that linkifies it, and in a form field.
func mintCredential() credential {
	secret := rand.Text()

	return credential{id: types.NewID(), secret: secret, hash: hashSecret(secret)}
}

// String is what the holder is given. A dot because it is in neither hex nor
// base32, so splitting needs no escaping.
func (c credential) String() string { return c.id.String() + "." + c.secret }

// splitCredential takes apart a presented "<id>.<secret>". The identifier is
// parsed strictly, so a flood of junk costs no reads.
func splitCredential(presented string) (types.ID, string, error) {
	rawID, secret, found := strings.Cut(presented, ".")
	if !found {
		return types.ID{}, "", fmt.Errorf("%w: it has no separator", errMalformed)
	}

	id, err := types.ParseID(rawID)
	if err != nil {
		return types.ID{}, "", fmt.Errorf("%w: %w", errMalformed, err)
	}

	// A floor rather than an exact length: rand.Text may grow in a later Go,
	// and a link minted by the binary before a deploy must still work after.
	if len(secret) < 20 {
		return types.ID{}, "", fmt.Errorf("%w: the secret is too short to be one", errMalformed)
	}

	return id, secret, nil
}
