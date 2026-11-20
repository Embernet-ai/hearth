package secret

// Encryption, checked at the edges rather than only down the middle.
//
// A round trip passing proves almost nothing: an implementation that returned
// the plaintext unchanged would pass it. What matters is that the wrong key
// fails, a tampered byte fails, the context binding actually binds, and two
// seals of the same value differ.

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
)

func testBox(t *testing.T) *Box {
	t.Helper()
	key := make([]byte, KeyBytes)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("key: %v", err)
	}
	b, err := New(key)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return b
}

func TestARoundTripReturnsWhatWentIn(t *testing.T) {
	b := testBox(t)
	const token = "1//09zX-refresh-token-value"

	sealed, err := b.SealString(token, "principal:abc")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(sealed, []byte(token)) {
		t.Fatal("the plaintext is present in the ciphertext, so this is not encryption")
	}
	got, err := b.OpenString(sealed, "principal:abc")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got != token {
		t.Errorf("got %q, want %q", got, token)
	}
}

func TestTheSameValueSealsDifferentlyEveryTime(t *testing.T) {
	b := testBox(t)
	first, _ := b.SealString("same", "ctx")
	second, _ := b.SealString("same", "ctx")

	// A fresh nonce per call. Equal ciphertexts would mean a fixed nonce, and a
	// repeated nonce under one key breaks GCM completely: it leaks the
	// authentication key, not just the message.
	if bytes.Equal(first, second) {
		t.Fatal("two seals of the same value are identical, so the nonce is not random")
	}
	for _, sealed := range [][]byte{first, second} {
		if got, err := b.OpenString(sealed, "ctx"); err != nil || got != "same" {
			t.Errorf("a seal did not open: %q %v", got, err)
		}
	}
}

func TestAnotherKeyCannotOpenIt(t *testing.T) {
	sealed, err := testBox(t).SealString("secret", "ctx")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := testBox(t).OpenString(sealed, "ctx"); !errors.Is(err, ErrBadCiphertext) {
		t.Fatalf("a different key opened it, or failed wrongly: %v", err)
	}
}

// The context binding is the thing that stops a ciphertext being moved between
// rows. Without it, lifting one person's sealed refresh token into another
// person's row would decrypt cleanly and connect the wrong Google account.
func TestACiphertextWillNotOpenUnderAnotherContext(t *testing.T) {
	b := testBox(t)
	sealed, _ := b.SealString("dana's refresh token", "principal:dana")

	if _, err := b.OpenString(sealed, "principal:mallory"); !errors.Is(err, ErrBadCiphertext) {
		t.Fatalf("a value sealed for one person opened for another: %v", err)
	}
	if got, err := b.OpenString(sealed, "principal:dana"); err != nil || got != "dana's refresh token" {
		t.Errorf("it does not open for its own context: %q %v", got, err)
	}
}

func TestATamperedByteIsRefused(t *testing.T) {
	b := testBox(t)
	sealed, _ := b.SealString("do not change me", "ctx")

	for _, at := range []int{0, len(sealed) / 2, len(sealed) - 1} {
		bad := bytes.Clone(sealed)
		bad[at] ^= 0x01
		if _, err := b.OpenString(bad, "ctx"); !errors.Is(err, ErrBadCiphertext) {
			t.Errorf("flipping a bit at %d was accepted: %v", at, err)
		}
	}
}

func TestSomethingTooShortIsRefusedRatherThanPanicking(t *testing.T) {
	b := testBox(t)
	for _, short := range [][]byte{nil, {}, {1, 2, 3}} {
		if _, err := b.Open(short, "ctx"); !errors.Is(err, ErrBadCiphertext) {
			t.Errorf("a %d byte value gave %v", len(short), err)
		}
	}
}

func TestANilBoxRefusesRatherThanStoringInTheClear(t *testing.T) {
	// This is the state a deployment is in with no key configured. It must
	// fail loudly, because the alternative is writing somebody's Google
	// refresh token into the database as plain text.
	var b *Box
	if _, err := b.SealString("x", "ctx"); !errors.Is(err, ErrNoKey) {
		t.Errorf("sealing with no key gave %v", err)
	}
	if _, err := b.OpenString([]byte("x"), "ctx"); !errors.Is(err, ErrNoKey) {
		t.Errorf("opening with no key gave %v", err)
	}
}

func TestKeysAreCheckedForLengthRatherThanStretched(t *testing.T) {
	good := base64.StdEncoding.EncodeToString(make([]byte, KeyBytes))
	if _, err := ParseKey(good); err != nil {
		t.Errorf("a valid 32 byte key was refused: %v", err)
	}
	// Accepting a short key by stretching it lets somebody configure eight
	// characters and believe they have 256 bits.
	short := base64.StdEncoding.EncodeToString([]byte("too short"))
	if _, err := ParseKey(short); err == nil {
		t.Error("a nine byte key was accepted")
	}
	if _, err := ParseKey("not base64 at all!!"); err == nil {
		t.Error("a value that is not base64 was accepted")
	}
	// Both alphabets and both paddings, because operators paste what their
	// tool produced.
	raw := make([]byte, KeyBytes)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if _, err := ParseKey(enc.EncodeToString(raw)); err != nil {
			t.Errorf("a key in one of the accepted encodings was refused: %v", err)
		}
	}
}
