package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/platform/secret"
)

// Keys is the VAPID pair Web Push signs with: the public point a browser subscribes against and the
// private scalar that proves a push came from this installation. Both are URL-safe base64 without
// padding, which is the form browsers and the sending library want.
type Keys struct {
	Public  string
	Private string
}

// KeysFromPEM turns a P-256 private key in PEM (the form a Kubernetes Secret or a podman secret holds)
// into the two strings Web Push wants.
func KeysFromPEM(pemBytes []byte) (Keys, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return Keys{}, errors.New("push: the VAPID key is not PEM")
	}
	var k *ecdsa.PrivateKey
	if ec, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		k = ec
	} else if any8, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		var ok bool
		if k, ok = any8.(*ecdsa.PrivateKey); !ok {
			return Keys{}, errors.New("push: the VAPID key is not an EC key")
		}
	} else {
		return Keys{}, errors.New("push: the VAPID key does not parse")
	}
	if k.Curve != elliptic.P256() {
		return Keys{}, errors.New("push: the VAPID key must be P-256")
	}
	ek, err := k.ECDH()
	if err != nil {
		return Keys{}, fmt.Errorf("push: %w", err)
	}
	return Keys{
		Public:  base64.RawURLEncoding.EncodeToString(ek.PublicKey().Bytes()),
		Private: base64.RawURLEncoding.EncodeToString(ek.Bytes()),
	}, nil
}

// LoadKeys finds the installation's VAPID key. A key file (the chart mounts one from a Secret that
// survives upgrades) wins. With no file the key lives in the database, made once by whichever process
// starts first and read back by every start after, so a Quadlet install needs nothing extra and an
// upgrade or a restart never strands a subscribed phone.
func LoadKeys(ctx context.Context, db *pgxpool.Pool, file string, box *secret.Box) (Keys, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return Keys{}, fmt.Errorf("push: read the VAPID key file: %w", err)
		}
		return KeysFromPEM(b)
	}
	if k, ok, err := readStored(ctx, db, box); err != nil || ok {
		return k, err
	}
	pemBytes, err := newPEM()
	if err != nil {
		return Keys{}, err
	}
	store, sealed := pemBytes, false
	if box != nil {
		if store, err = box.Seal(pemBytes, "push_vapid"); err != nil {
			return Keys{}, fmt.Errorf("push: seal the VAPID key: %w", err)
		}
		sealed = true
	}
	// Two starting together both try; the primary key lets one win and the other reads the winner's.
	if _, err := db.Exec(ctx,
		`INSERT INTO push_vapid (id, key_pem, sealed) VALUES (1, $1, $2) ON CONFLICT (id) DO NOTHING`,
		store, sealed); err != nil {
		return Keys{}, fmt.Errorf("push: store the VAPID key: %w", err)
	}
	k, ok, err := readStored(ctx, db, box)
	if err != nil {
		return Keys{}, err
	}
	if !ok {
		return Keys{}, errors.New("push: the VAPID key was not stored")
	}
	return k, nil
}

func readStored(ctx context.Context, db *pgxpool.Pool, box *secret.Box) (Keys, bool, error) {
	var raw []byte
	var sealed bool
	err := db.QueryRow(ctx, `SELECT key_pem, sealed FROM push_vapid WHERE id = 1`).Scan(&raw, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return Keys{}, false, nil
	}
	if err != nil {
		return Keys{}, false, fmt.Errorf("push: read the VAPID key: %w", err)
	}
	if sealed {
		if box == nil {
			return Keys{}, false, errors.New("push: the VAPID key was stored with HEARTH_ENCRYPTION_KEY, and none is set now")
		}
		plain, err := box.Open(raw, "push_vapid")
		if err != nil {
			return Keys{}, false, fmt.Errorf("push: open the VAPID key: %w", err)
		}
		raw = plain
	}
	k, err := KeysFromPEM(raw)
	return k, err == nil, err
}

func newPEM() ([]byte, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("push: generate the VAPID key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, fmt.Errorf("push: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}
