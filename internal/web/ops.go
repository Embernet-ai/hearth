package web

// The operator page for background work.
//
// A queue with no window into it is a queue nobody trusts, and the first
// question in an incident is always the same: is the work piling up, and if so
// is it because there is a lot of it or because nothing is running. Depth alone
// cannot tell those apart, which is why the age of the oldest ready job is the
// number this page leads with.

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/jobs"
	"github.com/Embernet-ai/hearth/internal/search"
)

// SelfTestKind is a job that always fails.
//
// It exists so the dead letter path can be proved on a real deployment rather
// than only in a test against a database on somebody's laptop. The house rule
// is that a guard is not trusted until it has been watched failing, and this is
// how that rule is applied to a mechanism whose whole job is to handle failure.
const SelfTestKind = "selftest.fail"

// selfTestAttempts is deliberately small. The point is to reach the dead letter
// table in seconds, not to demonstrate the full backoff curve.
const selfTestAttempts = 2

// SelfTestHandler always returns an error. Registered like any other handler,
// because a self test that took a special path through the worker would be
// testing the special path.
func SelfTestHandler() jobs.Handler {
	return func(_ context.Context, j *jobs.Job) error {
		return fmt.Errorf("self test: this job fails on purpose (attempt %d)", j.Attempts+1)
	}
}

// requireAdmin gates the administration pages: the owner, or a super admin.
//
// These were the owner's alone, from before super admin existed. A super admin
// sits one step below the owner and the owner delegates to them, so everything
// administrative is theirs too. The one thing they cannot do -- act on the
// owner's own account -- is enforced in auth, where accounts are changed.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	id := auth.From(r.Context())
	admin, err := s.auth.IsAdmin(r.Context(), id.OrgID, id.PrincipalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "resolve admin", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not check who may administer this")
		return false
	}
	if !admin {
		s.fail(w, r, http.StatusForbidden, "only the owner or a super admin can see this page")
		return false
	}
	return true
}

func (s *Server) getOpsJobs(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())

	stats, err := s.queue.Stats(r.Context(), id.OrgID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "job stats", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not read the queue")
		return
	}
	dead, err := s.queue.Dead(r.Context(), id.OrgID, 50)
	if err != nil {
		s.log.ErrorContext(r.Context(), "dead letters", "error", err)
	}

	p := page{
		Title: "Background work", IsAdmin: true,
		Stats: stats, Dead: dead, Routes: s.relay.Routes(),
		WorkerOn: s.workerOn,
	}
	if state, err := search.State(r.Context(), s.searchDB, id.OrgID); err == nil {
		p.IndexState = state
	} else {
		s.log.ErrorContext(r.Context(), "search index state", "error", err)
	}
	p.Engines = s.search.Ready(r.Context())

	s.render(w, r, "ops", p)
}

// postOpsBackfill rebuilds the search index from Postgres.
//
// It enqueues jobs rather than doing the work here, so a rebuild of a large
// instance is not a request that times out, and so the rebuild goes through the
// same path, retries, and operations page as an ordinary edit. An index that
// can only be rebuilt by a special code path is an index nobody has rebuilt.
func (s *Server) postOpsBackfill(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	if err := search.StartBackfill(r.Context(), s.queue, id.OrgID); err != nil {
		s.log.ErrorContext(r.Context(), "start backfill", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not start the backfill")
		return
	}
	s.kick(r)
	http.Redirect(w, r, "/ops/jobs", http.StatusSeeOther)
}

// postOpsSelfTest enqueues the job that always fails.
func (s *Server) postOpsSelfTest(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())

	if _, err := s.queue.Enqueue(r.Context(), jobs.Spec{
		OrgID: id.OrgID, Kind: SelfTestKind, MaxAttempts: selfTestAttempts,
		Payload: map[string]any{"requested_by": id.PrincipalID},
	}); err != nil {
		s.log.ErrorContext(r.Context(), "enqueue self test", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "could not enqueue the self test")
		return
	}
	s.kick(r)
	http.Redirect(w, r, "/ops/jobs", http.StatusSeeOther)
}

func (s *Server) postOpsRevive(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	deadID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "that is not a dead letter id")
		return
	}
	if _, err := s.queue.Revive(r.Context(), id.OrgID, deadID); err != nil {
		s.log.WarnContext(r.Context(), "revive refused", "dead", deadID, "error", err)
		s.fail(w, r, http.StatusNotFound, "no such dead letter")
		return
	}
	s.kick(r)
	http.Redirect(w, r, "/ops/jobs", http.StatusSeeOther)
}

func (s *Server) postOpsDiscard(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := auth.From(r.Context())
	deadID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "that is not a dead letter id")
		return
	}
	if err := s.queue.Discard(r.Context(), id.OrgID, deadID); err != nil {
		s.log.WarnContext(r.Context(), "discard refused", "dead", deadID, "error", err)
		s.fail(w, r, http.StatusNotFound, "no such dead letter")
		return
	}
	http.Redirect(w, r, "/ops/jobs", http.StatusSeeOther)
}

// kick nudges the worker so a job enqueued from a page runs now rather than at
// the next poll. Best effort by design: the poll interval is what makes the
// queue correct, and this only makes it prompt.
func (s *Server) kick(r *http.Request) {
	if s.notifyDB != nil {
		jobs.Kick(r.Context(), s.notifyDB)
	}
}
