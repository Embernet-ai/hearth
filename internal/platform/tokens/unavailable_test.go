package tokens

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
)

// keySet answers every signature check with err.
type keySet struct{ err error }

func (k keySet) VerifySignature(context.Context, string) ([]byte, error) { return nil, k.err }

func jwt(t *testing.T, iss, aud string) string {
	t.Helper()
	seg := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return seg(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." +
		seg(map[string]any{"iss": iss, "aud": aud, "sub": "s", "exp": time.Now().Add(time.Hour).Unix()}) + ".sig"
}

// A token the provider's keys could not be fetched for is not an invalid token:
// ErrUnavailable, so the door answers 503 and a client keeps a good token. A
// token whose signature simply fails is still ErrInvalid.
func TestUnreachableKeysAreNotAnInvalidToken(t *testing.T) {
	const iss, aud = "https://vord.example/realms/x", "hearth"
	check := func(ks keySet) error {
		v := FromVerifier(coreoidc.NewVerifier(iss, ks, &coreoidc.Config{ClientID: aud}), aud)
		_, err := v.Verify(context.Background(), jwt(t, iss, aud))
		return err
	}
	if err := check(keySet{fmt.Errorf("oidc: get keys failed %w", errors.New("dial tcp: connection refused"))}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unreachable keys came back as %v, want ErrUnavailable", err)
	}
	if err := check(keySet{errors.New("no keys match")}); !errors.Is(err, ErrInvalid) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("a signature that does not verify came back as %v, want ErrInvalid", err)
	}
}
