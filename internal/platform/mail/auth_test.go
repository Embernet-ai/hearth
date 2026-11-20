package mail

import (
	"net/smtp"
	"testing"
)

// The relay inside the cluster speaks SMTP with no TLS and requires a login.
// That login is allowed in clear for a cluster service name and nothing else.
func TestAClusterRelayGetsTheLoginWithoutTLS(t *testing.T) {
	const host = "mail-relay.mail.svc.cluster.local"
	auth := authFor(host, "hearth", "s3cret", false)

	mech, resp, err := auth.Start(&smtp.ServerInfo{Name: host, TLS: false, Auth: []string{"PLAIN"}})
	if err != nil {
		t.Fatalf("an in-cluster relay was refused the login: %v", err)
	}
	if mech != "PLAIN" || string(resp) != "\x00hearth\x00s3cret" {
		t.Fatalf("got %q %q, want PLAIN with the credential", mech, resp)
	}
	if _, err := auth.Next(nil, false); err != nil {
		t.Fatalf("finishing the exchange: %v", err)
	}
}

// Anything that is not a cluster service keeps net/smtp's refusal, so a typo or
// a hostile host is never handed the password over a plain connection.
func TestAnOutsideHostNeverGetsTheLoginInClear(t *testing.T) {
	for _, host := range []string{
		"smtp.gmail.com",
		"mail-relay",                         // bare: a search domain can send it anywhere
		"evil.svc.cluster.local.example.com", // only looks internal
	} {
		auth := authFor(host, "hearth", "s3cret", false)
		if _, _, err := auth.Start(&smtp.ServerInfo{Name: host, TLS: false, Auth: []string{"PLAIN"}}); err == nil {
			t.Errorf("%s was given the password without TLS", host)
		}
	}
}

// With STARTTLS on, even a cluster host goes through the standard check, which
// passes once the connection is encrypted.
func TestStartTLSKeepsTheStandardLogin(t *testing.T) {
	const host = "mail-relay.mail.svc.cluster.local"
	auth := authFor(host, "hearth", "s3cret", true)
	if _, ok := auth.(clusterPlainAuth); ok {
		t.Fatal("STARTTLS on, but the plaintext-allowed login was chosen")
	}
	if _, _, err := auth.Start(&smtp.ServerInfo{Name: host, TLS: true, Auth: []string{"PLAIN"}}); err != nil {
		t.Fatalf("an encrypted connection was refused: %v", err)
	}
}

// The credential is for the relay it was configured for and no other server.
func TestTheClusterLoginGoesOnlyToItsOwnHost(t *testing.T) {
	auth := authFor("mail-relay.mail.svc.cluster.local", "hearth", "s3cret", false)
	if _, _, err := auth.Start(&smtp.ServerInfo{Name: "other.mail.svc.cluster.local"}); err == nil {
		t.Fatal("the relay credential was offered to a different host")
	}
	if _, err := auth.Next([]byte("challenge"), true); err == nil {
		t.Fatal("an unexpected challenge was answered")
	}
}
