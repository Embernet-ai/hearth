package crmforms_test

import (
	"context"
	"testing"

	"github.com/Embernet-ai/hearth/internal/crmforms"
	"github.com/Embernet-ai/hearth/internal/forms"
)

type fake struct{ listed, read int }

func (f *fake) Forms(context.Context) ([]forms.Form, error) {
	f.listed++
	return []forms.Form{
		{ID: "f1", Name: "Expo lead form", Status: forms.InProgress, Questions: []forms.Question{{ID: "q1", Headline: "Your email"}, {ID: "q2", Headline: "What do you need?"}}},
		{ID: "f2", Name: "Draft", Status: forms.Draft},
	}, nil
}
func (f *fake) Responses(_ context.Context, id string, _, _ int) ([]forms.Response, error) {
	f.read++
	if id != "f1" {
		return nil, nil
	}
	return []forms.Response{
		{ID: "r1", Created: "2026-11-01T10:00:00Z", Finished: true, Answers: map[string]any{"q1": "Dana@Acme.example", "q2": []any{"Rewinds", "Repairs"}}},
		{ID: "r2", Created: "2026-11-02T10:00:00Z", Answers: map[string]any{"q1": "someone@else.example", "q2": "x"}},
		{ID: "r3", Created: "2026-11-05T10:00:00Z", Answers: map[string]any{"q1": "dana.w@acme.example"}},
	}, nil
}

func TestAContactSeesTheirOwnFormResponsesByAnyOfTheirAddresses(t *testing.T) {
	src := &fake{}
	s := crmforms.New(src)
	got, err := s.ForEmails(context.Background(), []string{"dana@acme.example", "dana.w@acme.example"})
	if err != nil || len(got) != 2 {
		t.Fatalf("entries = %+v (%v), want the two that are theirs", got, err)
	}
	if got[0].Created != "2026-11-05T10:00:00Z" || got[1].Form != "Expo lead form" || !got[1].Finished {
		t.Errorf("entries = %+v, want newest first", got)
	}
	if got[1].Answers[0].Question != "Your email" || got[1].Answers[1].Answer != "Rewinds, Repairs" {
		t.Errorf("answers = %+v, want the question's own words and a list joined", got[1].Answers)
	}
	if none, _ := s.ForEmails(context.Background(), []string{"nobody@x.example"}); len(none) != 0 {
		t.Errorf("a stranger's address matched %d", len(none))
	}
	if none, _ := s.ForEmails(context.Background(), nil); len(none) != 0 {
		t.Error("no address matched something")
	}
	if src.listed != 1 || src.read != 1 {
		t.Errorf("Formbricks was asked %d times for forms and %d for responses, want once each (a draft is never read)", src.listed, src.read)
	}
}
