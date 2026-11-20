package web

// The importer needs a slice of two services, not one.
//
// Documents come from docs and spaces come from plant, and the importer
// declares what it needs as one interface rather than taking both services and
// deciding for itself which to call. That keeps the dependency pointing one
// way: the importer says what it requires, and the caller is what knows how
// this application happens to be assembled.

import (
	"context"
	"errors"
	"time"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/channels"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/importer/clickup"
	"github.com/Embernet-ai/hearth/internal/notify"
	"github.com/Embernet-ai/hearth/internal/plant"
	"github.com/Embernet-ai/hearth/internal/timelog"
)

// importStore joins the document service to the space service for an import.
type importStore struct {
	docs.Store
	plant  plant.Store
	auth   *auth.Service
	time   timelog.Store
	chat   *channels.Service
	notify *notify.Service
}

// ProvisionPerson creates an account for somebody the source knows about.
func (s importStore) ProvisionPerson(ctx context.Context, orgID, externalID, displayName, email string) (string, error) {
	if s.auth == nil {
		return "", errNoPeople
	}
	return s.auth.ProvisionUser(ctx, orgID, externalID, displayName, email)
}

// People lets the importer turn a ClickUp assignee into a real principal.
func (s importStore) People(ctx context.Context, orgID string) ([]clickup.Person, error) {
	if s.auth == nil {
		return nil, errNoPeople
	}
	list, err := s.auth.People(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]clickup.Person, 0, len(list))
	for _, p := range list {
		out = append(out, clickup.Person{
			PrincipalID: p.PrincipalID, DisplayName: p.DisplayName, Email: p.Email,
		})
	}
	return out, nil
}

// CreateSpace mirrors a source hierarchy into the space tree.
//
// Returns the id alone rather than the Space, because that is all the importer
// wants and handing it the whole record would tie the importer to plant's
// types for no benefit.
func (s importStore) CreateSpace(ctx context.Context, orgID string, actor core.Subjects,
	name, kind string, parentID *string) (string, error) {

	if s.plant == nil {
		// Spaces are optional. An installation without the plant service still
		// imports its work; it simply arrives unfiled, which is where every
		// document lived before the importer built a tree.
		return "", errNoSpaces
	}
	sp, err := s.plant.CreateSpace(ctx, orgID, actor, name, kind, parentID)
	if err != nil {
		return "", err
	}
	return sp.ID, nil
}

// errNoSpaces is returned when there is no space service to file into.
var errNoSpaces = errors.New("web: spaces are not configured")

// errNoPeople is returned when there is no directory to match assignees against.
var errNoPeople = errors.New("web: the directory is not configured")

// errNoTimelog is returned when there is nowhere to record effort.
var errNoTimelog = errors.New("web: time tracking is not configured")

// LogTime records effort the source had already tracked.
func (s importStore) LogTime(ctx context.Context, orgID string, actor core.Subjects,
	docID string, minutes int, on time.Time, note string) error {

	if s.time == nil {
		return errNoTimelog
	}
	_, err := s.time.Log(ctx, orgID, actor, docID, minutes, on, note)
	return err
}

// SetEstimate carries the source's estimate onto the task.
func (s importStore) SetEstimate(ctx context.Context, orgID string, actor core.Subjects,
	docID string, minutes *int) error {

	if s.time == nil {
		return errNoTimelog
	}
	return s.time.SetEstimate(ctx, orgID, actor, docID, minutes)
}

// AddChecklistItem rebuilds a step the source carried, returning its id.
func (s importStore) AddChecklistItem(ctx context.Context, orgID string, actor core.Subjects,
	docID, text string) (string, error) {

	it, err := s.Store.AddChecklistItem(ctx, orgID, actor, docID, text, "")
	if err != nil {
		return "", err
	}
	return it.ID, nil
}

// PostComment carries a conversation that already happened.
func (s importStore) PostComment(ctx context.Context, orgID, principalID string,
	docID, body string, at time.Time, parentID string) (string, error) {

	if s.chat == nil {
		return "", errNoChat
	}
	return s.chat.PostImported(ctx, orgID, principalID,
		core.Ref{Type: core.EntityDocument, ID: docID}, body, at, parentID)
}

// Watch records that somebody was following this where it came from.
func (s importStore) Watch(ctx context.Context, principalID, docID string) error {
	if s.notify == nil {
		return nil
	}
	return s.notify.Watch(ctx, principalID, core.Ref{Type: core.EntityDocument, ID: docID}, "manual")
}

// errNoChat is returned when there is nowhere to put a comment.
var errNoChat = errors.New("web: messaging is not configured")

// ClearImportedTime removes the entries a previous import wrote, so a re-run
// replaces the hours rather than adding a second copy of them.
func (s importStore) ClearImportedTime(ctx context.Context, orgID, docID, note string) error {
	if s.time == nil {
		return nil
	}
	return s.time.DeleteImported(ctx, orgID, docID, note)
}

// SetAssignees records everybody a task was assigned to, beyond the one the
// task field holds.
func (s importStore) SetAssignees(ctx context.Context, docID string, principalIDs []string, primary string) error {
	return s.Store.SetAssignees(ctx, docID, principalIDs, primary)
}

// FindSpaceByName finds a space wherever somebody has filed it.
func (s importStore) FindSpaceByName(ctx context.Context, orgID, name string) (string, error) {
	if s.plant == nil {
		return "", nil
	}
	return s.plant.FindSpaceByName(ctx, orgID, name)
}
