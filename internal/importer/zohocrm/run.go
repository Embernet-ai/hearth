package zohocrm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config is one import run.
type Config struct {
	// BaseURL is the Hearth site, https://hearth.example.com, with no path.
	BaseURL string
	// Token is an API token with the write scope, and acts as a person who can
	// see and add CRM records.
	Token string
	// OwnerID is the person every company and contact is assigned to.
	OwnerID string
	// DryRun reads Hearth and the rows, writes nothing, and reports what it would do.
	DryRun bool
	// PerSecond paces requests so the API's limiter is never the thing that stops
	// a long run. Zero means 8.
	PerSecond float64
	// Manifest receives one JSON line per record the run made or found: kind,
	// id, external id and what happened. Ids only, no personal data. May be nil.
	Manifest io.Writer
	// Progress receives a line every few hundred records. May be nil.
	Progress io.Writer
	// HTTP is the client to use. Nil means one with a 60 second timeout.
	HTTP *http.Client
}

// Report is what a run did, in counts.
type Report struct {
	Rows              int
	Accounts          int
	CompaniesCreated  int
	CompaniesExisting int // already in Hearth by name, or made by an earlier run
	ContactsCreated   int
	ContactsExisting  int // already in Hearth by email, or made by an earlier run
	ContactsFailed    int
	NameMismatches    int // Contact Name disagreed with First and Last
	UnusedColumns     []string
	Errors            []string // ids and statuses only
}

type client struct {
	cfg  Config
	http *http.Client
	tick *time.Ticker
}

func newClient(cfg Config) *client {
	h := cfg.HTTP
	if h == nil {
		h = &http.Client{Timeout: 60 * time.Second}
	}
	rate := cfg.PerSecond
	if rate <= 0 {
		rate = 8
	}
	return &client{cfg: cfg, http: h, tick: time.NewTicker(time.Duration(float64(time.Second) / rate))}
}

// answer is the part of an API reply the importer reads.
type answer struct {
	ID         string `json:"id"`
	ExistingID string `json:"existing_id"`
	Total      int    `json:"total"`
	Companies  []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"companies"`
	Contacts []struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"contacts"`
}

// do sends one request. It waits for a tick first, and on 429 or a 5xx waits
// and tries again a few times.
func (c *client) do(ctx context.Context, method, path string, body any) (status int, replay bool, out answer, err error) {
	var payload []byte
	if body != nil {
		if payload, err = json.Marshal(body); err != nil {
			return 0, false, out, err
		}
	}
	for attempt := 0; ; attempt++ {
		select {
		case <-ctx.Done():
			return 0, false, out, ctx.Err()
		case <-c.tick.C:
		}
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.cfg.BaseURL, "/")+"/api"+path, bytes.NewReader(payload))
		if err != nil {
			return 0, false, out, err
		}
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		var raw []byte
		if err == nil {
			raw, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
			resp.Body.Close()
		}
		if err == nil && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			var a answer
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &a)
			}
			return resp.StatusCode, resp.Header.Get("Idempotent-Replay") == "true", a, nil
		}
		if attempt >= 6 {
			if err == nil {
				err = fmt.Errorf("answered %d", resp.StatusCode)
			}
			return 0, false, out, err
		}
		wait := time.Duration(attempt+1) * 3 * time.Second
		if err == nil {
			if s, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && s >= 0 {
				wait = time.Duration(s) * time.Second
			}
		}
		select {
		case <-ctx.Done():
			return 0, false, out, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// existing reads every company and contact the caller can see, keyed by
// lowercase name and lowercase email.
func (c *client) existing(ctx context.Context) (companies, contacts map[string]string, err error) {
	companies, contacts = map[string]string{}, map[string]string{}
	for _, kind := range []string{"companies", "contacts"} {
		for offset := 0; ; offset += 200 {
			st, _, a, err := c.do(ctx, http.MethodGet, "/v1/"+kind+"?limit=200&offset="+strconv.Itoa(offset), nil)
			if err != nil {
				return nil, nil, err
			}
			if st != http.StatusOK {
				return nil, nil, fmt.Errorf("zohocrm: list %s answered %d", kind, st)
			}
			for _, x := range a.Companies {
				companies[strings.ToLower(strings.TrimSpace(x.Name))] = x.ID
			}
			for _, x := range a.Contacts {
				if x.Email != "" {
					contacts[strings.ToLower(x.Email)] = x.ID
				}
			}
			if len(a.Companies)+len(a.Contacts) == 0 || offset+200 >= a.Total {
				break
			}
		}
	}
	return companies, contacts, nil
}

func (c *Config) log(format string, a ...any) {
	if c.Progress != nil {
		fmt.Fprintf(c.Progress, format+"\n", a...)
	}
}

func (c *Config) note(kind, id, ext, action string) {
	if c.Manifest == nil {
		return
	}
	line, _ := json.Marshal(map[string]string{"kind": kind, "id": id, "external_id": ext, "action": action})
	fmt.Fprintf(c.Manifest, "%s\n", line)
}

// Run imports rows. It returns the report even when it stops early with an error.
func Run(ctx context.Context, cfg Config, rows []Row) (*Report, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.Token) == "" || strings.TrimSpace(cfg.OwnerID) == "" {
		return nil, errors.New("zohocrm: a site URL, an API token and an owner id are required")
	}
	if _, err := url.Parse(cfg.BaseURL); err != nil {
		return nil, fmt.Errorf("zohocrm: bad site URL: %w", err)
	}
	rep := &Report{Rows: len(rows), UnusedColumns: UnusedColumns(rows)}
	for _, r := range rows {
		if cn := r.Get("Contact Name"); cn != "" && cn != ContactName(r) {
			rep.NameMismatches++
		}
	}
	accounts := Accounts(rows)
	rep.Accounts = len(accounts)
	cl := newClient(cfg)
	defer cl.tick.Stop()

	haveCompanies, haveContacts, err := cl.existing(ctx)
	if err != nil {
		return rep, err
	}

	// Companies first, so a contact can be put on one.
	companyID := map[string]string{} // account key -> Hearth id
	for i, a := range accounts {
		key := accountKey(a.ID, a.Name)
		if id, ok := haveCompanies[strings.ToLower(a.Name)]; ok {
			companyID[key] = id
			rep.CompaniesExisting++
			cfg.note("company", id, a.ID, "existing")
			continue
		}
		if cfg.DryRun {
			rep.CompaniesCreated++
			continue
		}
		st, replay, m, err := cl.do(ctx, http.MethodPost, "/v1/companies", CompanyBody(a.Name, a.ID, cfg.OwnerID))
		if err != nil {
			return rep, fmt.Errorf("zohocrm: company %s: %w", a.ID, err)
		}
		if (st != http.StatusCreated && st != http.StatusOK) || m.ID == "" {
			return rep, fmt.Errorf("zohocrm: company %s answered %d", a.ID, st)
		}
		companyID[key] = m.ID
		haveCompanies[strings.ToLower(a.Name)] = m.ID
		if replay {
			rep.CompaniesExisting++
			cfg.note("company", m.ID, a.ID, "existing")
		} else {
			rep.CompaniesCreated++
			cfg.note("company", m.ID, a.ID, "created")
		}
		if (i+1)%500 == 0 {
			cfg.log("companies %d of %d", i+1, len(accounts))
		}
	}

	for i, r := range rows {
		email := strings.ToLower(r.Get("Email"))
		ext := r.Get("Record Id")
		if id, ok := haveContacts[email]; ok && email != "" {
			rep.ContactsExisting++
			cfg.note("contact", id, ext, "existing")
			continue
		}
		if cfg.DryRun {
			rep.ContactsCreated++
			continue
		}
		cid := ""
		if k := AccountKey(r); k != "" {
			cid = companyID[k]
		}
		st, replay, m, err := cl.do(ctx, http.MethodPost, "/v1/contacts", ContactBody(r, cfg.OwnerID, cid))
		if err != nil {
			return rep, fmt.Errorf("zohocrm: contact %s: %w", ext, err)
		}
		switch {
		case (st == http.StatusCreated || st == http.StatusOK) && m.ID != "":
			if replay {
				rep.ContactsExisting++
				cfg.note("contact", m.ID, ext, "existing")
			} else {
				rep.ContactsCreated++
				cfg.note("contact", m.ID, ext, "created")
			}
			haveContacts[email] = m.ID
		case st == http.StatusConflict:
			rep.ContactsExisting++
			cfg.note("contact", m.ExistingID, ext, "existing")
		default:
			rep.ContactsFailed++
			rep.Errors = append(rep.Errors, fmt.Sprintf("contact %s: status %d", ext, st))
		}
		if (i+1)%500 == 0 {
			cfg.log("contacts %d of %d", i+1, len(rows))
		}
	}
	return rep, nil
}
