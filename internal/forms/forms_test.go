package forms

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestQuestionTextRoundTrips(t *testing.T) {
	text := "[text] Anything else?\n[choice] How did you hear about us? * :: A friend; Search; An event\n[multi] Pick some :: A; B\n[rating] How was it? *\n[nps] Would you recommend us?\n"
	qs := ParseQuestions(text)
	if len(qs) != 5 {
		t.Fatalf("parsed %d questions, want 5", len(qs))
	}
	if qs[1].Type != "multipleChoiceSingle" || !qs[1].Required || !reflect.DeepEqual(qs[1].Choices, []string{"A friend", "Search", "An event"}) {
		t.Errorf("choice question = %+v", qs[1])
	}
	if back := QuestionsText(qs); back != text {
		t.Errorf("round trip changed the text:\n%s\nwant\n%s", back, text)
	}
	if q := ParseQuestions("Just words\n[choice] One option :: Only"); q[0].Type != "openText" || q[1].Type != "openText" {
		t.Errorf("an untagged line and a choice with fewer than two options are text questions, got %+v", q)
	}
}

func TestDateAndScheduling(t *testing.T) {
	if got := Date("2026-12-24T23:00:00.000Z"); got != "2026-12-25" {
		t.Errorf("a day kept as 23:00Z the day before reads %q, want 2026-12-25", got)
	}
	f := Form{Status: Draft, PublishOn: "2026-12-24T23:00:00.000Z"}
	if !f.IsScheduled(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) || f.IsScheduled(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("a draft with a future publish day is scheduled, and not after it")
	}
	if (Form{Status: InProgress, PublishOn: f.PublishOn}).IsScheduled(time.Now()) {
		t.Error("a live form is not scheduled")
	}
}

// The forms app's management API as it answers: a key in x-api-key, the workspace from /me, status and the two days on update.
func TestClientAgainstTheAppsShape(t *testing.T) {
	var puts []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "k" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/v1/management/surveys" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[{"id":"s1","name":"Feedback","status":"inProgress","questions":[{"id":"q1","type":"openText","headline":{"default":"Anything else?"},"required":true}]}]}`))
		case r.URL.Path == "/api/v1/management/surveys/s1" && r.Method == http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			puts = append(puts, m)
			_, _ = w.Write([]byte(`{"data":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "k")
	list, err := c.Forms(context.Background())
	if err != nil || len(list) != 1 || list[0].Name != "Feedback" || list[0].Status != InProgress || len(list[0].Questions) != 1 || list[0].Questions[0].Headline != "Anything else?" || !list[0].Questions[0].Required {
		t.Fatalf("Forms = %+v, %v", list, err)
	}
	if err := c.SetStatus(context.Background(), "s1", Paused); err != nil || puts[0]["status"] != "paused" {
		t.Errorf("SetStatus sent %v, %v", puts, err)
	}
	if err := c.Schedule(context.Background(), "s1", "2026-12-25", ""); err != nil {
		t.Fatal(err)
	}
	if puts[1]["publishOn"] != "2026-12-25T12:00:00.000Z" || puts[1]["closeOn"] != nil {
		t.Errorf("Schedule sent %v, want midday UTC and a cleared close", puts[1])
	}
	if err := c.Schedule(context.Background(), "s1", "Christmas", ""); err == nil {
		t.Error("a date that is not a date is refused")
	}
	if err := New(srv.URL, "wrong").SetStatus(context.Background(), "s1", Paused); err == nil {
		t.Error("a refused key is an error")
	}
}

// A response is deleted with the call Formbricks documents, and every page of answers is read.
func TestDeleteResponseAndAllResponses(t *testing.T) {
	var deleted string
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v1/management/responses/"):
			deleted = strings.TrimPrefix(r.URL.Path, "/api/v1/management/responses/")
			_, _ = w.Write([]byte(`{"data":{}}`))
		case r.URL.Path == "/api/v1/management/responses":
			pages++
			n := 200
			if r.URL.Query().Get("skip") == "200" {
				n = 3
			}
			var sb strings.Builder
			sb.WriteString(`{"data":[`)
			for i := 0; i < n; i++ {
				if i > 0 {
					sb.WriteString(",")
				}
				sb.WriteString(`{"id":"r","createdAt":"2026-11-20T10:00:00Z","finished":true,"data":{"q1":"yes"}}`)
			}
			sb.WriteString(`]}`)
			_, _ = w.Write([]byte(sb.String()))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "k")
	ctx := context.Background()
	if err := c.DeleteResponse(ctx, "resp 1"); err != nil || deleted != "resp 1" {
		t.Fatalf("DeleteResponse: %v, deleted %q", err, deleted)
	}
	all, err := c.AllResponses(ctx, "s1")
	if err != nil || len(all) != 203 || pages != 2 {
		t.Fatalf("AllResponses: %d answers over %d pages, %v; want 203 over 2", len(all), pages, err)
	}
}
