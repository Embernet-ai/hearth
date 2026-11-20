package docs

// Sharing a document with a subject that is not a person.
//
// The whole of teams, from this package's point of view. A team is a grant
// subject exactly as a channel is, so sharing with one is the same authz call
// with a different subject kind, and there is no second permission path to keep
// in step with the first.

import (
	"context"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Share grants a subject access to a document.
//
// The caller has to be able to administer it. Being the owner of the
// installation is not the same as being allowed to share somebody's private
// document, and this is where that difference is decided rather than in a
// handler that happens to remember.
func (s *Service) Share(ctx context.Context, orgID string, actor core.Subjects,
	docID string, subject core.Subject, level core.AccessLevel, grantedBy string) error {

	ref := core.Ref{Type: core.EntityDocument, ID: docID}
	if err := s.authz.Require(ctx, actor, ref, core.AccessAdmin); err != nil {
		// Not found rather than forbidden when they cannot even see it, so this
		// endpoint is not a way to ask which document ids exist.
		if viewErr := s.authz.Require(ctx, actor, ref, core.AccessView); viewErr != nil {
			return ErrNotFound
		}
		return err
	}
	return s.authz.Grant(ctx, orgID, ref, subject, level, grantedBy)
}

// Unshare takes it away again.
func (s *Service) Unshare(ctx context.Context, orgID string, actor core.Subjects,
	docID string, subject core.Subject, actorID string) error {

	ref := core.Ref{Type: core.EntityDocument, ID: docID}
	if err := s.authz.Require(ctx, actor, ref, core.AccessAdmin); err != nil {
		if viewErr := s.authz.Require(ctx, actor, ref, core.AccessView); viewErr != nil {
			return ErrNotFound
		}
		return err
	}
	return s.authz.Revoke(ctx, orgID, ref, subject, actorID)
}
