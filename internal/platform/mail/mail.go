// Package mail sends through the estate's own relay.
//
// net/smtp, and nothing else. A transactional email provider is a hyperscaler
// with a nicer name: an outbound dependency, an account to keep paid, and a
// copy of every notification anybody here ever received sitting in somebody
// else's database. The estate already runs a relay.
//
// It is OPTIONAL. With no host configured, Configured reports false and every
// caller skips silently rather than filling the dead letter table with mail
// that was never going to be sent.
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// Config describes the relay.
type Config struct {
	// Host is host:port. Empty disables sending entirely.
	Host string
	// From is the envelope sender. Required when Host is set: a relay that
	// accepts an empty sender will still have every message filed as spam.
	From string
	// Username and Password are optional. An internal relay usually has
	// neither and authenticates by source address.
	Username string
	Password string
	// StartTLS upgrades the connection. On by default for anything that is not
	// plainly local.
	StartTLS bool
	Timeout  time.Duration
}

// Sender talks to the relay.
type Sender struct {
	cfg Config
}

func New(cfg Config) *Sender {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	return &Sender{cfg: cfg}
}

// Configured reports whether there is anywhere to send.
func (s *Sender) Configured() bool { return s != nil && s.cfg.Host != "" && s.cfg.From != "" }

// Send delivers one plain text message.
//
// Plain text, no HTML alternative. A notification is one sentence and a link,
// and an HTML email is a second rendering to keep in step, a set of client
// quirks to work around, and the reason most notification mail looks like
// marketing.
func (s *Sender) Send(ctx context.Context, to, subject, body string) error {
	return s.SendWithHeaders(ctx, to, subject, body, nil)
}

// SendWithHeaders is Send with extra headers, which exists for exactly one
// caller: a reply needs Message-Id, In-Reply-To, and References or it arrives
// in the sender's client as an unrelated message and they conclude the address
// does not work.
//
// The header names and values are checked rather than trusted. A newline in
// either is header injection, and the values here come from a database that a
// message from outside wrote to.
func (s *Sender) SendWithHeaders(ctx context.Context, to, subject, body string,
	headers map[string]string) error {
	if !s.Configured() {
		return nil
	}
	if _, err := mail.ParseAddress(to); err != nil {
		// A malformed address is not worth retrying, and a job that retries it
		// eight times fills the dead letter table with the same typo.
		return fmt.Errorf("mail: %q is not an address: %w", to, err)
	}

	msg := s.compose(to, subject, body, headers)

	// Dialled with a deadline rather than left to the default, because a relay
	// that accepts a connection and then says nothing will otherwise hold a
	// worker until the lease lapses.
	deadline := time.Now().Add(s.cfg.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	dialer := &net.Dialer{Deadline: deadline}
	conn, err := dialer.DialContext(ctx, "tcp", s.cfg.Host)
	if err != nil {
		return fmt.Errorf("mail: dial %s: %w", s.cfg.Host, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("mail: set deadline: %w", err)
	}

	host, _, err := net.SplitHostPort(s.cfg.Host)
	if err != nil {
		host = s.cfg.Host
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("mail: greet %s: %w", s.cfg.Host, err)
	}
	defer func() { _ = c.Quit() }()

	if s.cfg.StartTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("mail: starttls: %w", err)
			}
		} else {
			// Asked for and not offered. Refusing is right: silently sending
			// credentials and message bodies in clear because the server did
			// not advertise an extension is exactly the failure that setting
			// exists to prevent.
			return errors.New("mail: STARTTLS was required and the relay does not offer it")
		}
	}
	if s.cfg.Username != "" {
		if err := c.Auth(authFor(host, s.cfg.Username, s.cfg.Password, s.cfg.StartTLS)); err != nil {
			return fmt.Errorf("mail: authenticate: %w", err)
		}
	}

	if err := c.Mail(s.envelopeFrom()); err != nil {
		return fmt.Errorf("mail: sender rejected: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("mail: recipient %s rejected: %w", to, err)
	}
	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: begin data: %w", err)
	}
	if _, err := wc.Write([]byte(msg)); err != nil {
		return fmt.Errorf("mail: write message: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("mail: relay rejected the message: %w", err)
	}
	return nil
}

// envelopeFrom is the address only, without a display name, because that is
// what MAIL FROM takes.
func (s *Sender) envelopeFrom() string {
	if a, err := mail.ParseAddress(s.cfg.From); err == nil {
		return a.Address
	}
	return s.cfg.From
}

// compose builds the message.
//
// The header values are sanitised for newlines. A subject assembled from a
// document title is attacker-influenced text, and a newline in it injects
// arbitrary headers, which is how a notification becomes a way to send mail to
// somebody else entirely.
func (s *Sender) compose(to, subject, body string, extra map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", header(s.cfg.From))
	fmt.Fprintf(&b, "To: %s\r\n", header(to))
	fmt.Fprintf(&b, "Subject: %s\r\n", header(subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")

	// Threading, when the caller has any. Written in a fixed order rather than
	// by ranging the map, because a message whose headers shuffle between
	// sends is a message some spam filters score differently every time.
	auto := true
	for _, name := range []string{"Message-Id", "In-Reply-To", "References"} {
		v, ok := extra[name]
		if !ok || strings.TrimSpace(v) == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\r\n", headerName(name), header(v))
		if name == "In-Reply-To" {
			// A reply to a person is not auto-generated. Marking it so is how
			// an answer somebody was waiting for gets filed as noise.
			auto = false
		}
	}
	if auto {
		// So a notification goes to a person rather than into a relay's null
		// account, and so an out of office does not answer it.
		b.WriteString("Auto-Submitted: auto-generated\r\n")
	}
	b.WriteString("\r\n")
	// Dot-stuffing: a line that is a single full stop ends the DATA command.
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, ".") {
			line = "." + line
		}
		b.WriteString(line + "\r\n")
	}
	return b.String()
}

// headerName keeps a name from being anything other than a name.
//
// Nothing takes a header name from outside today, and this is what makes that
// stay true when somebody adds the first caller that does.
func headerName(v string) string {
	var b strings.Builder
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "X-Hearth"
	}
	return b.String()
}

func header(v string) string {
	v = strings.ReplaceAll(v, "\r", " ")
	v = strings.ReplaceAll(v, "\n", " ")
	if len(v) > 400 {
		v = v[:400]
	}
	return strings.TrimSpace(v)
}

// authFor picks how to present the relay credential.
//
// net/smtp's PlainAuth refuses to send a password over a connection that is not
// TLS unless the server is localhost, and that refusal is right for anything on
// the internet. It is wrong for one case: a relay inside the cluster that does
// not offer TLS at all, because the hop never leaves the overlay network. That
// is how outbound mail works on an estate whose provider blocks SMTP ports --
// an in-cluster relay takes SMTP and delivers over an HTTPS API.
//
// So a plaintext login is allowed only when STARTTLS is off AND the host is a
// cluster service name. Anything else gets PlainAuth and its refusal, so a
// mistyped or hostile host can never be handed the password in clear.
func authFor(host, username, password string, startTLS bool) smtp.Auth {
	if !startTLS && inCluster(host) {
		return clusterPlainAuth{username: username, password: password, host: host}
	}
	return smtp.PlainAuth("", username, password, host)
}

// inCluster reports whether host is a Kubernetes service name, which only
// resolves inside the cluster. Bare names are not accepted: a search domain can
// send them anywhere.
func inCluster(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return strings.HasSuffix(h, ".svc.cluster.local") || strings.HasSuffix(h, ".svc")
}

// clusterPlainAuth is SASL PLAIN without the TLS requirement, for an in-cluster
// relay only. It keeps net/smtp's other check: the credential goes to the host
// it was configured for and no other.
type clusterPlainAuth struct {
	username, password, host string
}

func (a clusterPlainAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if server.Name != a.host {
		return "", nil, errors.New("mail: wrong host name for the relay credential")
	}
	return "PLAIN", []byte("\x00" + a.username + "\x00" + a.password), nil
}

func (a clusterPlainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("mail: unexpected challenge from the relay")
	}
	return nil, nil
}
