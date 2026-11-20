// Package push rings a phone that has the app closed.
//
// It is Web Push (RFC 8030, signed with VAPID, RFC 8292): the browser hands the page a subscription, the
// page gives it to the server, and the server posts an encrypted message to the push service the browser
// belongs to (Google's, Mozilla's, Apple's or Microsoft's), which wakes the installed app's service
// worker. There is no extra service to run. The sender is part of the Hearth process, the key is made
// once and kept, and the only thing the deployment must allow is an outbound connection to those push
// services on port 443.
//
// Every push is a copy of a notification Hearth already writes to somebody's inbox, sent from the same
// place (notify.Service.Deliver), so a push exists only for something that is also in the inbox and
// carries the same sentence. A push never carries the excerpt or any body of the item: the title is what
// shows on a locked screen, and the click opens the item behind sign-in.
package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The kinds a person chooses between. They are the notifications Hearth already has, grouped the way a
// person thinks of them: approvals are the requests that wait on somebody's decision (hours, time off,
// departments) and the answers to them, which the inbox files under assigned and changed.
const (
	KindAssigned  = "assigned"
	KindMentioned = "mentioned"
	KindCommented = "commented"
	KindChanged   = "changed"
	KindDueSoon   = "due_soon"
	KindApprovals = "approvals"
)

// Kind is one thing a person can choose to be rung for.
type Kind struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Help  string `json:"help"`
}

// Kinds is every kind, in the order the settings list them.
var Kinds = []Kind{
	{KindAssigned, "Assigned to me", "Somebody gives you a work order or a task."},
	{KindMentioned, "Mentions", "Somebody mentions you in a comment or a channel."},
	{KindCommented, "Comments", "A new comment on something you follow."},
	{KindChanged, "Changes", "A status or field changes on something you follow, or a rule tells you."},
	{KindDueSoon, "Due soon", "Something assigned to you is due today or soon."},
	{KindApprovals, "Approvals", "A request waits on your decision, or yours was decided."},
}

// ValidKind reports whether id names a kind.
func ValidKind(id string) bool {
	for _, k := range Kinds {
		if k.ID == id {
			return true
		}
	}
	return false
}

func allKinds() []string {
	out := make([]string, len(Kinds))
	for i, k := range Kinds {
		out[i] = k.ID
	}
	return out
}

// Subscription is what a browser gives the page when it subscribes.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// Device is one subscribed browser, as the person sees it in their list.
type Device struct {
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	Endpoint  string     `json:"endpoint"`
	CreatedAt time.Time  `json:"created_at"`
	LastOKAt  *time.Time `json:"last_ok_at,omitempty"`
	// LastStatus is the push service's last answer, 0 before the first send.
	LastStatus int `json:"last_status"`
}

// Message is one push. Only the title-level sentence and where to go; never the item's content.
type Message struct {
	PrincipalID string
	Kind        string
	Body        string
	URL         string
	// Tag lets a newer push about the same thing replace the older one on the lock screen.
	Tag string
}

// SendOptions are the per-message knobs of Web Push.
type SendOptions struct {
	TTL     int
	Urgency string
}

// Sender posts one encrypted message to one subscription and returns the push service's status. It is a
// field so a test can put a fake push endpoint behind the real encryption, and so the one place that
// talks to the outside is a single function.
type Sender func(ctx context.Context, payload []byte, sub Subscription, o SendOptions) (int, error)

// Options configures a Service.
type Options struct {
	Keys Keys
	// Subject is the contact the push services see: a mailto: or an https: address (RFC 8292).
	Subject string
	Log     *slog.Logger
	// Send replaces the real sender. Tests only.
	Send Sender
	// ValidEndpoint replaces the push-service allow list. Tests only.
	ValidEndpoint func(string) bool
}

// Service keeps subscriptions and sends pushes.
type Service struct {
	db    *pgxpool.Pool
	keys  Keys
	send  Sender
	valid func(string) bool
	log   *slog.Logger

	wg  sync.WaitGroup
	sem chan struct{}
}

// New builds a Service. Keys come from LoadKeys.
func New(db *pgxpool.Pool, o Options) (*Service, error) {
	if o.Keys.Public == "" || o.Keys.Private == "" {
		return nil, errors.New("push: no VAPID key")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	s := &Service{db: db, keys: o.Keys, log: o.Log, send: o.Send, valid: o.ValidEndpoint, sem: make(chan struct{}, 8)}
	if s.valid == nil {
		s.valid = ValidPushEndpoint
	}
	if s.send == nil {
		subject := o.Subject
		client := &http.Client{Timeout: 15 * time.Second}
		s.send = func(ctx context.Context, payload []byte, sub Subscription, so SendOptions) (int, error) {
			resp, err := webpush.SendNotificationWithContext(ctx, payload,
				&webpush.Subscription{Endpoint: sub.Endpoint, Keys: webpush.Keys{P256dh: sub.Keys.P256dh, Auth: sub.Keys.Auth}},
				&webpush.Options{
					HTTPClient: client, Subscriber: subject,
					VAPIDPublicKey: o.Keys.Public, VAPIDPrivateKey: o.Keys.Private,
					TTL: so.TTL, Urgency: webpush.Urgency(so.Urgency),
				})
			if err != nil {
				return 0, err
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			return resp.StatusCode, nil
		}
	}
	return s, nil
}

// PublicKey is what the page subscribes against.
func (s *Service) PublicKey() string { return s.keys.Public }

// pushHosts are the browsers' push services: Chrome, Edge and Opera (Google's), Firefox, Safari and the
// Windows one. A subscription naming any other host is refused, otherwise a signed-in person could aim
// this server's outbound posts at anything it can reach.
//
// Google's is the odd one. Chrome is handed fcm.googleapis.com on most machines and a numbered Google
// host such as jmt17.google.com on others (seen from a stock Chromium), and both answer at /fcm/. So a
// google.com host is accepted for that path and no other.
var pushHosts = []string{"fcm.googleapis.com", "push.services.mozilla.com", "notify.windows.com", "push.apple.com"}

// ValidPushEndpoint reports whether raw is an https address on one of the browsers' push services.
func ValidPushEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if strings.HasSuffix(h, ".google.com") && strings.HasPrefix(u.Path, "/fcm/") {
		return true
	}
	for _, p := range pushHosts {
		if h == p || strings.HasSuffix(h, "."+p) {
			return true
		}
	}
	return false
}

// ErrBadSubscription is a subscription this server will not keep.
var ErrBadSubscription = errors.New("push: that is not a subscription from a browser's push service")

// Subscribe keeps a device for a person. The same endpoint turned on by somebody else moves to them: a
// browser has one subscription, and ringing two people's accounts from one handset is the wrong default.
func (s *Service) Subscribe(ctx context.Context, orgID, principalID string, sub Subscription, label string) error {
	if !s.valid(sub.Endpoint) || sub.Keys.P256dh == "" || sub.Keys.Auth == "" ||
		len(sub.Endpoint) > 2048 || len(sub.Keys.P256dh) > 256 || len(sub.Keys.Auth) > 256 {
		return ErrBadSubscription
	}
	label = strings.TrimSpace(label)
	if len(label) > 80 {
		label = label[:80]
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO push_subscriptions (id, org_id, principal_id, endpoint, p256dh, auth, label)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (endpoint) DO UPDATE
		    SET org_id = EXCLUDED.org_id, principal_id = EXCLUDED.principal_id,
		        p256dh = EXCLUDED.p256dh, auth = EXCLUDED.auth, label = EXCLUDED.label`,
		uuid.NewString(), orgID, principalID, sub.Endpoint, sub.Keys.P256dh, sub.Keys.Auth, label)
	if err != nil {
		return fmt.Errorf("push: subscribe: %w", err)
	}
	return nil
}

// Unsubscribe forgets one of the person's own devices, by its endpoint or its id.
func (s *Service) Unsubscribe(ctx context.Context, principalID, endpointOrID string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM push_subscriptions WHERE principal_id = $1 AND (endpoint = $2 OR id::text = $2)`,
		principalID, endpointOrID)
	if err != nil {
		return fmt.Errorf("push: unsubscribe: %w", err)
	}
	return nil
}

// Devices lists a person's subscribed browsers.
func (s *Service) Devices(ctx context.Context, principalID string) ([]Device, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, label, endpoint, created_at, last_ok_at, COALESCE(last_status, 0)
		FROM push_subscriptions WHERE principal_id = $1 ORDER BY created_at`, principalID)
	if err != nil {
		return nil, fmt.Errorf("push: devices: %w", err)
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.Label, &d.Endpoint, &d.CreatedAt, &d.LastOKAt, &d.LastStatus); err != nil {
			return nil, fmt.Errorf("push: scan device: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Kinds returns the kinds a person is rung for: every kind until they choose.
func (s *Service) Kinds(ctx context.Context, principalID string) ([]string, error) {
	var kinds []string
	err := s.db.QueryRow(ctx, `SELECT kinds FROM push_prefs WHERE principal_id = $1`, principalID).Scan(&kinds)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return allKinds(), nil
	case err != nil:
		return nil, fmt.Errorf("push: kinds: %w", err)
	}
	return kinds, nil
}

// SetKinds saves which kinds ring. An empty list is allowed: the devices stay, and nothing rings.
func (s *Service) SetKinds(ctx context.Context, principalID string, kinds []string) error {
	seen := map[string]bool{}
	clean := []string{}
	for _, k := range kinds {
		if !ValidKind(k) {
			return fmt.Errorf("push: %q is not a notification kind", k)
		}
		if !seen[k] {
			seen[k] = true
			clean = append(clean, k)
		}
	}
	sort.Strings(clean)
	_, err := s.db.Exec(ctx, `
		INSERT INTO push_prefs (principal_id, kinds) VALUES ($1,$2)
		ON CONFLICT (principal_id) DO UPDATE SET kinds = EXCLUDED.kinds, updated_at = now()`,
		principalID, clean)
	if err != nil {
		return fmt.Errorf("push: set kinds: %w", err)
	}
	return nil
}

type payload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag,omitempty"`
	Kind  string `json:"kind,omitempty"`
}

// Notify sends one push to every device of one person, after the call returns: a push is never part of the
// write that caused it, and a slow push service must not slow a status change. A kind the person turned
// off is not sent. It is best effort by design, because the inbox row it copies is the record.
func (s *Service) Notify(ctx context.Context, m Message) {
	ctx = context.WithoutCancel(ctx)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.sem <- struct{}{}
		defer func() { <-s.sem }()
		ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		defer cancel()
		on, err := s.Kinds(ctx, m.PrincipalID)
		if err != nil {
			s.log.Error("push: read kinds", "error", err)
			return
		}
		if !contains(on, m.Kind) {
			return
		}
		devs, err := s.targets(ctx, m.PrincipalID, "")
		if err != nil {
			s.log.Error("push: read devices", "error", err)
			return
		}
		body, _ := json.Marshal(payload{Title: "Hearth", Body: m.Body, URL: m.URL, Tag: m.Tag, Kind: m.Kind})
		for _, d := range devs {
			s.deliver(ctx, body, d, SendOptions{TTL: 12 * 3600, Urgency: "normal"})
		}
	}()
}

// Flush waits for the pushes started so far. A server never needs it; a test does.
func (s *Service) Flush() { s.wg.Wait() }

// TestResult is what the "send a test" button reports.
type TestResult struct {
	Sent    int `json:"sent"`
	Removed int `json:"removed"`
	Failed  int `json:"failed"`
}

// Test sends a push to one of a person's devices (or all of them when endpoint is empty) and says what
// happened, so a person who cannot see anything arrive can see why. It ignores the kinds and quiet hours:
// asking for a test is asking to be rung.
func (s *Service) Test(ctx context.Context, principalID, endpoint string) (TestResult, error) {
	devs, err := s.targets(ctx, principalID, endpoint)
	if err != nil {
		return TestResult{}, err
	}
	body, _ := json.Marshal(payload{Title: "Hearth", Body: "Notifications are on for this device.", URL: "/inbox",
		Tag: "hearth-test", Kind: "test"})
	var r TestResult
	for _, d := range devs {
		switch s.deliver(ctx, body, d, SendOptions{TTL: 300, Urgency: "high"}) {
		case outcomeSent:
			r.Sent++
		case outcomeGone:
			r.Removed++
		default:
			r.Failed++
		}
	}
	return r, nil
}

type target struct {
	Subscription
	id string
}

func (s *Service) targets(ctx context.Context, principalID, endpoint string) ([]target, error) {
	q := `SELECT id::text, endpoint, p256dh, auth FROM push_subscriptions WHERE principal_id = $1`
	args := []any{principalID}
	if endpoint != "" {
		q += ` AND (endpoint = $2 OR id::text = $2)`
		args = append(args, endpoint)
	}
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("push: targets: %w", err)
	}
	defer rows.Close()
	var out []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.Endpoint, &t.Keys.P256dh, &t.Keys.Auth); err != nil {
			return nil, fmt.Errorf("push: scan target: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type outcome int

const (
	outcomeSent outcome = iota
	outcomeGone
	outcomeFailed
)

// deliver sends one message and acts on the answer. A 404 or 410 is the push service saying the browser
// dropped the subscription (the app was removed, or permission was withdrawn), so it is forgotten rather
// than tried on every notification forever. Anything else is recorded and left alone: a 5xx or a 429 is
// the service's trouble, and a 401 or 403 means the key does not match the subscription, which a person
// fixes by turning push off and on, not by the server deleting their device.
func (s *Service) deliver(ctx context.Context, body []byte, t target, o SendOptions) outcome {
	status, err := s.send(ctx, body, t.Subscription, o)
	if err != nil {
		s.log.Warn("push: send failed", "error", err)
		s.record(ctx, t.id, 0)
		return outcomeFailed
	}
	switch {
	case status == http.StatusNotFound || status == http.StatusGone:
		if _, err := s.db.Exec(ctx, `DELETE FROM push_subscriptions WHERE id = $1`, t.id); err != nil {
			s.log.Error("push: forget a dead subscription", "error", err)
		}
		return outcomeGone
	case status >= 200 && status < 300:
		s.record(ctx, t.id, status)
		return outcomeSent
	default:
		s.log.Warn("push: the push service refused", "status", status)
		s.record(ctx, t.id, status)
		return outcomeFailed
	}
}

func (s *Service) record(ctx context.Context, id string, status int) {
	ok := status >= 200 && status < 300
	_, err := s.db.Exec(ctx, `
		UPDATE push_subscriptions
		   SET last_status = NULLIF($2, 0), last_ok_at = CASE WHEN $3 THEN now() ELSE last_ok_at END
		 WHERE id = $1`, id, status, ok)
	if err != nil {
		s.log.Error("push: record the answer", "error", err)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
