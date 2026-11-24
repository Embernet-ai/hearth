// Package crmforms shows, on a contact's page, the answers they gave on Formbricks forms.
//
// Formbricks keeps the forms and their responses. A response is the contact's when one of its answers is their email address
// (the form asks for it), so the match is made on the address Hearth holds for them, or one of their other addresses. The
// forms and their responses are read once a minute and shared, so opening a contact does not call Formbricks.
package crmforms

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Embernet-ai/hearth/internal/forms"
)

// Source is the slice of the Formbricks client this reads.
type Source interface {
	Forms(ctx context.Context) ([]forms.Form, error)
	Responses(ctx context.Context, formID string, limit, skip int) ([]forms.Response, error)
}

// QA is one question and what was answered.
type QA struct{ Question, Answer string }

// Entry is one response from a person.
type Entry struct {
	Form     string
	Created  string
	Finished bool
	Answers  []QA
}

// Service reads and matches.
type Service struct {
	src Source
	ttl time.Duration

	mu     sync.Mutex
	loaded time.Time
	all    []loaded
}

type loaded struct {
	form      forms.Form
	responses []forms.Response
}

// New returns a Service that rereads Formbricks at most once a minute.
func New(src Source) *Service { return &Service{src: src, ttl: time.Minute} }

const (
	maxForms     = 40
	maxResponses = 200
)

func (s *Service) load(ctx context.Context) ([]loaded, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.all != nil && time.Since(s.loaded) < s.ttl {
		return s.all, nil
	}
	fs, err := s.src.Forms(ctx)
	if err != nil {
		return nil, fmt.Errorf("crmforms: list forms: %w", err)
	}
	if len(fs) > maxForms {
		fs = fs[:maxForms]
	}
	out := make([]loaded, 0, len(fs))
	for _, f := range fs {
		if f.Status == forms.Draft {
			continue
		}
		rs, err := s.src.Responses(ctx, f.ID, maxResponses, 0)
		if err != nil {
			return nil, fmt.Errorf("crmforms: responses of %s: %w", f.Name, err)
		}
		out = append(out, loaded{form: f, responses: rs})
	}
	s.all, s.loaded = out, time.Now()
	return out, nil
}

func text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, x := range t {
			parts = append(parts, text(x))
		}
		return strings.Join(parts, ", ")
	}
	return fmt.Sprint(v)
}

// ForEmails is the responses that carry one of these addresses, newest first.
func (s *Service) ForEmails(ctx context.Context, emails []string) ([]Entry, error) {
	want := map[string]bool{}
	for _, e := range emails {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			want[e] = true
		}
	}
	if len(want) == 0 {
		return nil, nil
	}
	all, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, l := range all {
		label := map[string]string{}
		for _, q := range l.form.Questions {
			label[q.ID] = q.Headline
		}
		for _, r := range l.responses {
			hit := false
			for _, v := range r.Answers {
				if want[strings.ToLower(strings.TrimSpace(text(v)))] {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
			e := Entry{Form: l.form.Name, Created: r.Created, Finished: r.Finished}
			keys := make([]string, 0, len(r.Answers))
			for k := range r.Answers {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				q := label[k]
				if q == "" {
					q = k
				}
				if a := text(r.Answers[k]); a != "" {
					e.Answers = append(e.Answers, QA{q, a})
				}
			}
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out, nil
}
