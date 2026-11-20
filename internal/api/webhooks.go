package api

// Outgoing webhooks, off the outbox.
//
// Off the outbox and not out of the request that made the change, for the same
// reason notifications and search indexing are: a webhook fired inline tells
// somebody about a write that then rolled back, and it makes saving a work
// order as slow and as failure-prone as the slowest receiver anybody has
// configured.
//
// # Signing
//
// A URL is not a secret. Anybody who learns one can post to the receiver and
// claim a machine went down, so every delivery carries an HMAC of the exact
// body over a shared secret, plus a timestamp so an old body cannot be
// replayed. Both are in one header, and the receiver checks both or neither.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/jobs"
)

// DeliverKind is the job that posts one delivery.
const DeliverKind = "webhook.deliver"

// SignatureHeader carries the version, the timestamp, and the digest.
const SignatureHeader = "X-Hearth-Signature"

// maxReplayAge is how old a signature may be. Five minutes is long enough for
// a retry and short enough that a captured body is not useful tomorrow.
const maxReplayAge = 5 * time.Minute

// Webhook is one configured receiver.
type Webhook struct {
	ID      string
	OrgID   string
	URL     string
	Secret  string
	Events  []string
	Enabled bool
	// Recent is the last few attempts, for the page that answers "why did it
	// stop working".
	Recent []Delivery
}

// Delivery is one attempt.
type Delivery struct {
	ID          string
	WebhookID   string
	Event       string
	EntityType  string
	EntityID    *string
	Status      string
	Code        int
	Attempts    int
	LastError   string
	CreatedAt   time.Time
	DeliveredAt *time.Time
}

// OK reports whether it landed.
func (d Delivery) OK() bool { return d.Status == "delivered" }

// Webhooks lists the receivers for an org.
func (s *Service) Webhooks(ctx context.Context, orgID string) ([]*Webhook, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, url, secret, events, enabled
		FROM webhooks WHERE org_id=$1 ORDER BY url`, orgID)
	if err != nil {
		return nil, fmt.Errorf("api: list webhooks: %w", err)
	}
	defer rows.Close()

	var out []*Webhook
	for rows.Next() {
		h := &Webhook{OrgID: orgID}
		if err := rows.Scan(&h.ID, &h.URL, &h.Secret, &h.Events, &h.Enabled); err != nil {
			return nil, fmt.Errorf("api: scan webhook: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, h := range out {
		h.Recent, _ = s.Deliveries(ctx, orgID, h.ID, 5)
	}
	return out, nil
}

// SaveWebhook creates or replaces a receiver.
func (s *Service) SaveWebhook(ctx context.Context, orgID, actorID string, h Webhook) (*Webhook, error) {
	h.URL = strings.TrimSpace(h.URL)
	if !strings.HasPrefix(h.URL, "http://") && !strings.HasPrefix(h.URL, "https://") {
		return nil, fmt.Errorf("%w: a webhook needs an http or https URL", ErrInvalid)
	}
	if h.Events == nil {
		h.Events = []string{}
	}

	if h.ID == "" {
		// The secret is generated rather than typed. A secret somebody chose
		// is a secret somebody reused, and there is no reason for a human to
		// pick this one.
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			return nil, fmt.Errorf("api: generate secret: %w", err)
		}
		h.ID = uuid.NewString()
		h.OrgID = orgID
		h.Secret = "whsec_" + base64.RawURLEncoding.EncodeToString(raw)
		h.Enabled = true
		if _, err := s.db.Exec(ctx, `
			INSERT INTO webhooks (id, org_id, url, secret, events, enabled, created_by)
			VALUES ($1,$2,$3,$4,$5,true,$6)`,
			h.ID, orgID, h.URL, h.Secret, h.Events, actorID); err != nil {
			return nil, fmt.Errorf("api: create webhook: %w", err)
		}
		return &h, nil
	}

	ct, err := s.db.Exec(ctx, `
		UPDATE webhooks SET url=$3, events=$4, enabled=$5 WHERE id=$1 AND org_id=$2`,
		h.ID, orgID, h.URL, h.Events, h.Enabled)
	if err != nil {
		return nil, fmt.Errorf("api: update webhook: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return &h, nil
}

// DeleteWebhook removes a receiver and its delivery log.
func (s *Service) DeleteWebhook(ctx context.Context, orgID, id string) error {
	ct, err := s.db.Exec(ctx, `DELETE FROM webhooks WHERE id=$1 AND org_id=$2`, id, orgID)
	if err != nil {
		return fmt.Errorf("api: delete webhook: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Deliveries reads the log for one receiver.
func (s *Service) Deliveries(ctx context.Context, orgID, webhookID string, limit int) ([]Delivery, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
		SELECT id::text, webhook_id::text, event, entity_type, entity_id::text,
		       status, code, attempts, last_error, created_at, delivered_at
		FROM webhook_deliveries
		WHERE org_id=$1 AND webhook_id=$2
		ORDER BY created_at DESC LIMIT $3`, orgID, webhookID, limit)
	if err != nil {
		return nil, fmt.Errorf("api: deliveries: %w", err)
	}
	defer rows.Close()

	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.WebhookID, &d.Event, &d.EntityType, &d.EntityID,
			&d.Status, &d.Code, &d.Attempts, &d.LastError,
			&d.CreatedAt, &d.DeliveredAt); err != nil {
			return nil, fmt.Errorf("api: scan delivery: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// -----------------------------------------------------------------------------
// Dispatch
// -----------------------------------------------------------------------------

// Dispatcher turns outbox events into deliveries.
type Dispatcher struct {
	db     *pgxpool.Pool
	queue  *jobs.Queue
	client *http.Client
	log    *slog.Logger
}

func NewDispatcher(db *pgxpool.Pool, q *jobs.Queue, log *slog.Logger) *Dispatcher {
	return &Dispatcher{
		db: db, queue: q, log: log,
		client: &http.Client{
			// Bounded, because a receiver that accepts a connection and then
			// says nothing would otherwise hold a worker until its lease
			// lapses.
			Timeout: 15 * time.Second,
		},
	}
}

// Register wires the handlers onto a worker.
func (d *Dispatcher) Register(h interface {
	Handle(kind string, fn jobs.Handler)
}) {
	h.Handle(FanKind, d.handleFan)
	h.Handle(DeliverKind, d.handleDeliver)
}

// FanKind is the job that decides which receivers care about one event.
const FanKind = "webhook.fan"

// Routes subscribe to everything.
//
// Everything, because a webhook is configured by somebody outside this codebase
// and the list of events they might want is not knowable here. The filtering
// happens per receiver.
func Routes() []jobs.Route {
	var out []jobs.Route
	for _, event := range []string{
		jobs.EventCreated, jobs.EventUpdated, jobs.EventDeleted,
		jobs.EventStatus, jobs.EventAssigned,
	} {
		out = append(out, jobs.Route{
			Event: event, EntityType: string(core.EntityDocument), JobKind: FanKind,
		})
	}
	return out
}

// handleFan writes one pending delivery per interested receiver, then queues
// each one separately.
//
// Separately, so a receiver that is down does not delay a receiver that is up,
// and so a retry retries one delivery rather than all of them.
func (d *Dispatcher) handleFan(ctx context.Context, j *jobs.Job) error {
	var p jobs.EventPayload
	if err := j.Decode(&p); err != nil {
		return err
	}

	rows, err := d.db.Query(ctx, `
		SELECT id::text FROM webhooks
		WHERE org_id=$1 AND enabled
		  AND (cardinality(events) = 0 OR $2 = ANY(events))`, j.OrgID, p.Event)
	if err != nil {
		return fmt.Errorf("api: find webhooks: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("api: scan webhook: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, id := range ids {
		deliveryID := uuid.NewString()
		// ON CONFLICT DO NOTHING against the unique index on (webhook, event),
		// so a redelivered fan job produces one delivery rather than a second.
		var stored string
		err := d.db.QueryRow(ctx, `
			INSERT INTO webhook_deliveries (id, org_id, webhook_id, event_id, event,
			                                entity_type, entity_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (webhook_id, event_id) DO NOTHING
			RETURNING id::text`,
			deliveryID, j.OrgID, id, p.EventID, p.Event, p.EntityType, nullable(p.EntityID)).
			Scan(&stored)
		if errors.Is(err, pgx.ErrNoRows) {
			// Already queued by an earlier attempt at this same job.
			continue
		}
		if err != nil {
			return fmt.Errorf("api: record delivery: %w", err)
		}
		if _, err := d.queue.Enqueue(ctx, jobs.Spec{
			OrgID: j.OrgID, Kind: DeliverKind,
			Payload: map[string]any{"delivery_id": stored},
		}); err != nil {
			return fmt.Errorf("api: queue delivery: %w", err)
		}
	}
	return nil
}

// handleDeliver posts one body and records what came back.
func (d *Dispatcher) handleDeliver(ctx context.Context, j *jobs.Job) error {
	var p struct {
		DeliveryID string `json:"delivery_id"`
	}
	if err := j.Decode(&p); err != nil {
		return err
	}

	var (
		url, secret, event, entityType, status string
		entityID                               *string
		attempts                               int
		createdAt                              time.Time
	)
	err := d.db.QueryRow(ctx, `
		SELECT w.url, w.secret, dl.event, dl.entity_type, dl.entity_id::text,
		       dl.status, dl.attempts, dl.created_at
		FROM webhook_deliveries dl
		JOIN webhooks w ON w.id = dl.webhook_id
		WHERE dl.id = $1 AND dl.org_id = $2`, p.DeliveryID, j.OrgID).
		Scan(&url, &secret, &event, &entityType, &entityID, &status, &attempts, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// The receiver was deleted between the fan and the delivery. Not a
		// failure worth retrying.
		return nil
	}
	if err != nil {
		return fmt.Errorf("api: load delivery: %w", err)
	}
	if status == "delivered" {
		// Already sent by an earlier attempt at this same job. At-least-once
		// delivery makes this the normal case for a redelivered job rather
		// than an anomaly.
		return nil
	}

	body, err := json.Marshal(map[string]any{
		"id": p.DeliveryID, "event": event,
		"entity_type": entityType, "entity_id": entityID,
		"occurred_at": createdAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("api: encode delivery: %w", err)
	}

	code, sendErr := d.post(ctx, url, secret, body)
	attempts++

	if sendErr == nil && code >= 200 && code < 300 {
		_, _ = d.db.Exec(ctx, `
			UPDATE webhook_deliveries
			   SET status='delivered', code=$2, attempts=$3, delivered_at=now(), last_error=''
			 WHERE id=$1`, p.DeliveryID, code, attempts)
		return nil
	}

	reason := fmt.Sprintf("HTTP %d", code)
	if sendErr != nil {
		reason = sendErr.Error()
	}
	// Recorded as failed on every attempt rather than only on the last. The
	// queue's own retry is what decides whether there will be another, and a
	// row that says pending while eight attempts have bounced is a row that
	// tells an operator nothing.
	_, _ = d.db.Exec(ctx, `
		UPDATE webhook_deliveries SET status='failed', code=$2, attempts=$3, last_error=$4
		 WHERE id=$1`, p.DeliveryID, code, attempts, truncate(reason, 500))

	// Returned as an error so the queue retries with its own backoff and its
	// own dead letter table, rather than this package growing a second one.
	return fmt.Errorf("api: deliver to %s: %s", url, reason)
}

// post sends one signed body.
func (d *Dispatcher) post(ctx context.Context, url, secret string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "hearth-webhooks/1")
	req.Header.Set(SignatureHeader, Sign(secret, time.Now(), body))

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// Drained and discarded. A response body nobody reads keeps the connection
	// from being reused, and these are all to the same few hosts.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}

// Sign builds the signature header.
//
// The timestamp is inside the signed material, not beside it. A signature over
// the body alone can be replayed forever by anybody who captured one delivery,
// and a timestamp that is not signed can simply be edited.
func Sign(secret string, at time.Time, body []byte) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify checks a signature, as a receiver would.
//
// Here rather than only in a document, so the acceptance suite verifies the
// same way a real receiver has to and any mistake shows up on this side.
func Verify(secret, header string, body []byte, now time.Time) bool {
	var ts, digest string
	for _, part := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "t":
			ts = value
		case "v1":
			digest = value
		}
	}
	if ts == "" || digest == "" {
		return false
	}
	seconds, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	age := now.Sub(time.Unix(seconds, 0))
	if age < -maxReplayAge || age > maxReplayAge {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte{'.'})
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(digest), []byte(want))
}

func nullable(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
