package playground

import (
	"context"
	"fmt"
	"time"

	"github.com/Embernet-ai/hearth/internal/channels"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/timeoff"
)

// fill builds seed.go's content in a new playground, through the ordinary
// services and as the people in it -- the playground self for structure and
// lessons, each made-up colleague for their own comments and hours -- so it
// passes the same checks a person's request does. people maps seed keys to
// principal ids; "you" is the playground self.
func (s *Service) fill(ctx context.Context, orgID string, people map[string]string, now time.Time) error {
	self := people["you"]
	actor, err := s.authz.SubjectsFor(ctx, orgID, self)
	if err != nil {
		return fmt.Errorf("playground: you: %w", err)
	}
	as := map[string]core.Subjects{"you": actor}
	subjectsOf := func(key string) (core.Subjects, error) {
		if sub, ok := as[key]; ok {
			return sub, nil
		}
		sub, err := s.authz.SubjectsFor(ctx, orgID, people[key])
		if err != nil {
			return core.Subjects{}, fmt.Errorf("playground: %s: %w", key, err)
		}
		as[key] = sub
		return sub, nil
	}
	day := func(n int) *time.Time {
		t := time.Date(now.Year(), now.Month(), now.Day(), 17, 0, 0, 0, now.Location()).AddDate(0, 0, n)
		return &t
	}

	// Spaces, parents first (seed.go lists them that way).
	spaces := map[string]string{}
	top := map[string]string{} // space key -> its department's key
	for _, sp := range seedSpaces {
		var parent *string
		if sp.Parent != "" {
			id := spaces[sp.Parent]
			parent = &id
			top[sp.Key] = top[sp.Parent]
		} else {
			top[sp.Key] = sp.Key
		}
		created, err := s.plant.CreateSpace(ctx, orgID, actor, sp.Name, plant.KindFolder, parent)
		if err != nil {
			return fmt.Errorf("playground: space %s: %w", sp.Name, err)
		}
		spaces[sp.Key] = created.ID
	}

	// Colleagues: their department, the access that comes with it, managers.
	reach := map[string]map[string]bool{} // person key -> departments they can see
	grant := func(key, dept string) error {
		if reach[key] == nil {
			reach[key] = map[string]bool{}
		}
		if reach[key][dept] || key == "you" {
			return nil
		}
		if err := s.authz.GrantSpace(ctx, orgID, self, people[key], spaces[dept]); err != nil {
			return fmt.Errorf("playground: give %s %s: %w", key, dept, err)
		}
		reach[key][dept] = true
		delete(as, key) // their subjects change with the grant
		return nil
	}
	for _, p := range seedPeople {
		if err := s.authz.SetDepartments(ctx, orgID, self, people[p.Key], []string{spaces[p.Dept]}); err != nil {
			return fmt.Errorf("playground: department for %s: %w", p.Name, err)
		}
		if err := grant(p.Key, p.Dept); err != nil {
			return err
		}
		if p.Manager {
			if err := s.authz.AddManager(ctx, orgID, self, spaces[p.Dept], people[p.Key]); err != nil {
				return fmt.Errorf("playground: manager %s: %w", p.Name, err)
			}
		}
	}

	// By title, so a subtask can name the task it sits inside.
	taskIDs := map[string]string{}
	for _, t := range seedTasks {
		spaceID := spaces[t.Space]
		fields := &docs.TaskFields{Status: t.Status, Priority: t.Priority}
		if t.Assignee != "" {
			id := people[t.Assignee]
			fields.AssigneeID = &id
		}
		if t.Due != 0 {
			fields.DueAt = day(t.Due)
		}
		if t.Status == docs.StatusDone {
			fields.CompletedAt = day(-2)
		}
		var parent *string
		if t.Parent != "" {
			id, ok := taskIDs[t.Parent]
			if !ok {
				return fmt.Errorf("playground: %q sits inside %q, which is not listed before it", t.Title, t.Parent)
			}
			parent = &id
		}
		d, err := s.docs.Create(ctx, orgID, actor, docs.CreateParams{
			Kind: docs.KindTask, Title: t.Title, Body: t.Body, SpaceID: &spaceID, Task: fields, ParentID: parent,
		})
		if err != nil {
			return fmt.Errorf("playground: task %q: %w", t.Title, err)
		}
		taskIDs[t.Title] = d.ID
		for _, step := range t.Checklist {
			if _, err := s.docs.AddChecklistItem(ctx, orgID, actor, d.ID, step, ""); err != nil {
				return fmt.Errorf("playground: checklist on %q: %w", t.Title, err)
			}
		}
		for _, c := range t.Comments {
			// Somebody commenting from another department can see this one.
			if err := grant(c.By, top[t.Space]); err != nil {
				return err
			}
			by, err := subjectsOf(c.By)
			if err != nil {
				return err
			}
			if _, err := s.channels.Post(ctx, orgID, by, channels.PostParams{
				Entity: core.Ref{Type: core.EntityDocument, ID: d.ID}, Body: c.Body,
			}); err != nil {
				return fmt.Errorf("playground: comment on %q: %w", t.Title, err)
			}
		}
		if t.Minutes > 0 && t.Assignee != "" {
			by, err := subjectsOf(t.Assignee)
			if err != nil {
				return err
			}
			if _, err := s.time.Log(ctx, orgID, by, d.ID, t.Minutes, *day(-1), "Worked on it"); err != nil {
				return fmt.Errorf("playground: time on %q: %w", t.Title, err)
			}
		}
	}

	// A request waiting on a decision, for the managers' lesson. "You" hold
	// People in your playground, which is what lets you decide it.
	for _, r := range seedTimeOff {
		start := *day(r.In)
		if _, err := s.timeoff.Ask(ctx, orgID, people[r.By], timeoff.Params{
			StartsOn: start, EndsOn: start.AddDate(0, 0, r.Length-1), Kind: r.Kind,
			Reason: r.Reason, Acknowledged: true,
		}); err != nil {
			return fmt.Errorf("playground: time off for %s: %w", r.By, err)
		}
	}

	for _, doc := range seedDocs {
		spaceID := spaces[doc.Space]
		if _, err := s.docs.Create(ctx, orgID, actor, docs.CreateParams{
			Kind: doc.Kind, Title: doc.Title, Body: doc.Body, SpaceID: &spaceID,
		}); err != nil {
			return fmt.Errorf("playground: document %q: %w", doc.Title, err)
		}
	}

	for _, l := range seedLessons {
		spaceID := spaces[l.Track]
		d, err := s.docs.Create(ctx, orgID, actor, docs.CreateParams{
			Kind: docs.KindTask, Title: l.Title, Body: l.Intro, SpaceID: &spaceID,
			Task: &docs.TaskFields{Status: docs.StatusTodo, Priority: 1, AssigneeID: &self},
		})
		if err != nil {
			return fmt.Errorf("playground: lesson %q: %w", l.Title, err)
		}
		for _, st := range l.Steps {
			if _, err := s.docs.AddChecklistItem(ctx, orgID, actor, d.ID, st.Do, "Where: "+st.Where); err != nil {
				return fmt.Errorf("playground: step %q: %w", st.Do, err)
			}
		}
	}
	return nil
}
