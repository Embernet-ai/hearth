// Package secret encrypts values that have to be read back.
//
// Almost nothing in Hearth needs this, and that is deliberate. Session tokens,
// API tokens, provisioning tokens and calendar feed tokens are all HASHED: they
// are checked by comparing a hash, never read back, so a database that leaks
// hands over nothing usable. Hashing is strictly better wherever it is possible
// and it is possible almost everywhere.
//
// A third party's refresh token is the exception. It has to be presented to
// Google, verbatim, months after it was issued, so it cannot be hashed. It is
// also a long-lived credential to somebody's mail and calendar, which makes it
// the single most dangerous thing this application will ever store.
//
// So it is encrypted with a key that lives in the environment rather than in
// the database, and the two are compromised separately. A stolen backup is not
// a pile of working Google credentials unless the key went with it.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// KeyBytes is the required key length. AES-256.
const KeyBytes = 32

var (
	// ErrNoKey means the deployment has not configured one. Callers turn this
	// into "the integration is unavailable" rather than storing anything in
	// the clear.
	ErrNoKey = errors.New("secret: no encryption key is configured")
	// ErrBadCiphertext covers a value that will not open: truncated, corrupted,
	// or encrypted under a different key. All three are the same to a caller
	// and none of them should be distinguished to anybody else.
	ErrBadCiphertext = errors.New("secret: this value cannot be decrypted")
)

// Box seals and opens values.
type Box struct{ aead cipher.AEAD }

// ParseKey reads a base64 key, standard or URL alphabet, padded or not.
//
// Generated with `openssl rand -base64 32`. The length is checked rather than
// hashed to size: accepting a short key by stretching it would let somebody
// configure eight characters and believe they had 256 bits.
func ParseKey(encoded string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if raw, err := enc.DecodeString(encoded); err == nil {
			if len(raw) != KeyBytes {
				return nil, fmt.Errorf(
					"secret: the key decodes to %d bytes and must be %d. "+
						"Generate one with: openssl rand -base64 32", len(raw), KeyBytes)
			}
			return raw, nil
		}
	}
	return nil, errors.New("secret: the key is not valid base64")
}

// New returns a Box for a 32 byte key.
func New(key []byte) (*Box, error) {
	if len(key) != KeyBytes {
		return nil, fmt.Errorf("secret: key is %d bytes, want %d", len(key), KeyBytes)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts, returning nonce || ciphertext.
//
// A fresh random nonce per call, prepended rather than stored separately. GCM
// is catastrophically broken by a repeated nonce under the same key -- it leaks
// the authentication key, not merely one message -- so it is never derived from
// anything countable like a row id or a timestamp.
//
// context is bound as additional data. It is not secret and it is not stored:
// it is re-supplied on Open, so a ciphertext lifted from one person's row and
// pasted into another's fails to open rather than quietly decrypting into
// somebody else's Google account.
func (b *Box) Seal(plaintext []byte, context string) ([]byte, error) {
	if b == nil {
		return nil, ErrNoKey
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("secret: generate nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, plaintext, []byte(context)), nil
}

// Open decrypts what Seal produced, with the same context.
func (b *Box) Open(sealed []byte, context string) ([]byte, error) {
	if b == nil {
		return nil, ErrNoKey
	}
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, ErrBadCiphertext
	}
	plain, err := b.aead.Open(nil, sealed[:n], sealed[n:], []byte(context))
	if err != nil {
		// Deliberately not wrapped. The underlying error distinguishes a bad
		// key from a corrupted message, and neither is anybody's business.
		return nil, ErrBadCiphertext
	}
	return plain, nil
}

// SealString and OpenString are the common case.
func (b *Box) SealString(s, context string) ([]byte, error) { return b.Seal([]byte(s), context) }

func (b *Box) OpenString(sealed []byte, context string) (string, error) {
	plain, err := b.Open(sealed, context)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
