package offline

// Housekeeping for the record of what the phones did.
//
// The table is written once per queued action and read only by the person who
// queued it, so it is never hot. It is also never cleaned up by anything else,
// and a table nothing deletes from is a table that is fine for two years and
// then is the largest one in the database.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Embernet-ai/hearth/internal/jobs"
)

// KindPrune is the recurring tidy-up.
const KindPrune = "offline.prune"

// Keep is how long a settled operation is worth keeping.
//
// Ninety days, because the question it answers is "did what I did on my phone
// last month actually go in", and nobody asks that about the spring. Unsettled
// operations are never pruned, whatever their age.
const Keep = 90 * 24 * time.Hour

// pruneInterval is how often the sweep runs. Daily: this deletes rows that
// became eligible at some point in the previous twenty-four hours, and there
// is nothing to be gained by noticing sooner.
const pruneInterval = 24 * time.Hour

// Housekeeper runs the sweep.
type Housekeeper struct {
	svc   *Service
	queue *jobs.Queue
	log   *slog.Logger
}

func NewHousekeeper(svc *Service, q *jobs.Queue, log *slog.Logger) *Housekeeper {
	return &Housekeeper{svc: svc, queue: q, log: log}
}

// Register wires the handler onto a worker.
func (h *Housekeeper) Register(w interface {
	Handle(kind string, fn jobs.Handler)
}) {
	w.Handle(KindPrune, h.handle)
}

// StartAll queues the sweep for every org. Keyed, so restarting a replica does
// not multiply it.
func (h *Housekeeper) StartAll(ctx context.Context) error {
	rows, err := h.svc.db.Query(ctx, `SELECT id::text FROM orgs WHERE playground_of IS NULL`)
	if err != nil {
		return fmt.Errorf("offline: list orgs: %w", err)
	}
	defer rows.Close()
	var orgs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("offline: scan org: %w", err)
		}
		orgs = append(orgs, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, org := range orgs {
		if _, err := h.queue.Enqueue(ctx, jobs.Spec{
			OrgID: org, Kind: KindPrune, UniqueKey: "offline_prune",
		}); err != nil {
			return err
		}
	}
	return nil
}

func (h *Housekeeper) handle(ctx context.Context, j *jobs.Job) error {
	n, err := h.svc.Prune(ctx, j.OrgID, Keep)
	if err != nil {
		return err
	}
	if n > 0 {
		h.log.InfoContext(ctx, "pruned settled offline operations", "rows", n)
	}
	// Chain the next one rather than depending on a scheduler, the same way the
	// maintenance sweep and the digest do. A second thing that can be down is a
	// second thing that can be down.
	_, err = h.queue.Enqueue(ctx, jobs.Spec{
		OrgID: j.OrgID, Kind: KindPrune, UniqueKey: "offline_prune",
		RunAfter: time.Now().Add(pruneInterval),
	})
	return err
}
