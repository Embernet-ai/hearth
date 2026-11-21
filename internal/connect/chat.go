package connect

// Chat notifications: a person's Hearth notifications copied into a Google Chat space or a Teams chat of their own.
//
// Both products take a post from an incoming webhook, which needs no OAuth and no scopes, so this connector asks
// for nothing from the provider. A webhook address is a credential (whoever holds it can post), so it is sealed
// like a token, and it is only ever called at the vendor's own hosts: a person cannot point Hearth at an address
// inside the installation's network by pasting it here.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/push"
)

// Sealer is the slice of the secret box this needs.
type Sealer interface {
	SealString(plain, context string) ([]byte, error)
	OpenString(sealed []byte, context string) (string, error)
}

// ValidateWebhook checks an address is one the vendor issues.
func ValidateWebhook(p Provider, raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", errors.New("that is not an https address")
	}
	host := strings.ToLower(u.Hostname())
	switch p {
	case Google:
		if host != "chat.googleapis.com" || !strings.HasPrefix(u.Path, "/v1/spaces/") {
			return "", errors.New("a Google Chat webhook looks like https://chat.googleapis.com/v1/spaces/.../messages?key=...")
		}
	case Microsoft:
		ok := false
		for _, suf := range []string{".webhook.office.com", ".logic.azure.com", ".powerplatform.com", ".azure-api.net"} {
			if strings.HasSuffix(host, suf) {
				ok = true
			}
		}
		if !ok {
			return "", errors.New("a Teams webhook is the address the Workflows app gives for a chat or channel")
		}
	default:
		return "", errors.New("unknown provider")
	}
	return u.String(), nil
}

// SetChatHook saves a person's webhook, after checking it is the vendor's and that the connector is on.
func (s *Service) SetChatHook(ctx context.Context, box Sealer, orgID, principalID string, p Provider, raw string) error {
	if box == nil {
		return errors.New("connect: no encryption key is configured, so a webhook cannot be stored safely")
	}
	on, err := s.Enabled(ctx, orgID, p, Chat)
	if err != nil {
		return err
	}
	if !on {
		return ErrDisabled
	}
	addr, err := ValidateWebhook(p, raw)
	if err != nil {
		return err
	}
	sealed, err := box.SealString(addr, "chat:"+principalID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO connector_chat_hooks (principal_id, org_id, provider, webhook)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (principal_id) DO UPDATE SET provider=EXCLUDED.provider, webhook=EXCLUDED.webhook,
		       last_ok_at=NULL, last_error=''`, principalID, orgID, string(p), sealed)
	return err
}

// ClearChatHook forgets it.
func (s *Service) ClearChatHook(ctx context.Context, principalID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM connector_chat_hooks WHERE principal_id=$1`, principalID)
	return err
}

// ChatHook is a person's webhook, without the address.
type ChatHook struct {
	Provider  Provider
	LastOKAt  *time.Time
	LastError string
}

// ChatHookFor reads whether a person has one.
func (s *Service) ChatHookFor(ctx context.Context, principalID string) (*ChatHook, error) {
	h := &ChatHook{}
	var p string
	err := s.db.QueryRow(ctx, `SELECT provider, last_ok_at, last_error FROM connector_chat_hooks WHERE principal_id=$1`,
		principalID).Scan(&p, &h.LastOKAt, &h.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	h.Provider = Provider(p)
	return h, nil
}

// ChatNotifier copies notifications to a person's chat. It wraps whatever already rings their devices, so turning
// the connector on adds a channel and takes nothing away.
type ChatNotifier struct {
	svc     *Service
	box     Sealer
	http    *http.Client
	baseURL string
	next    interface {
		Notify(ctx context.Context, m push.Message)
	}
}

// NewChatNotifier builds one. next may be nil.
func (s *Service) NewChatNotifier(box Sealer, baseURL string, next interface {
	Notify(ctx context.Context, m push.Message)
}) *ChatNotifier {
	return &ChatNotifier{svc: s, box: box, baseURL: strings.TrimRight(baseURL, "/"), next: next,
		http: &http.Client{Timeout: 10 * time.Second}}
}

// Notify rings the devices as before, then copies to chat when this person has a webhook and the connector is on.
func (n *ChatNotifier) Notify(ctx context.Context, m push.Message) {
	if n.next != nil {
		n.next.Notify(ctx, m)
	}
	if n.box == nil || m.PrincipalID == "" {
		return
	}
	var sealed []byte
	var p, org string
	err := n.svc.db.QueryRow(ctx, `SELECT webhook, provider, org_id::text FROM connector_chat_hooks WHERE principal_id=$1`,
		m.PrincipalID).Scan(&sealed, &p, &org)
	if err != nil {
		return
	}
	if on, _ := n.svc.Enabled(ctx, org, Provider(p), Chat); !on {
		return
	}
	addr, err := n.box.OpenString(sealed, "chat:"+m.PrincipalID)
	if err != nil {
		return
	}
	link := ""
	if m.URL != "" {
		link = n.baseURL + m.URL
	}
	go func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		err := n.post(cctx, Provider(p), addr, m.Body, link)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		_, _ = n.svc.db.Exec(cctx, `UPDATE connector_chat_hooks SET last_error=$2,
			last_ok_at = CASE WHEN $2 = '' THEN now() ELSE last_ok_at END WHERE principal_id=$1`, m.PrincipalID, msg)
	}()
}

func chatBody(p Provider, text, link string) ([]byte, error) {
	if p == Google {
		t := text
		if link != "" {
			t += "\n" + link
		}
		return json.Marshal(map[string]string{"text": t})
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard", "version": "1.4",
		"body": []any{map[string]any{"type": "TextBlock", "text": text, "wrap": true}},
	}
	if link != "" {
		card["actions"] = []any{map[string]any{"type": "Action.OpenUrl", "title": "Open in Hearth", "url": link}}
	}
	return json.Marshal(map[string]any{"type": "message", "attachments": []any{
		map[string]any{"contentType": "application/vnd.microsoft.card.adaptive", "contentUrl": nil, "content": card}}})
}

func (n *ChatNotifier) post(ctx context.Context, p Provider, addr, text, link string) error {
	body, err := chatBody(p, text, link)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, addr, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	resp, err := n.http.Do(req)
	if err != nil {
		return errors.New("could not reach the chat service")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("the chat service answered %d", resp.StatusCode)
	}
	return nil
}
