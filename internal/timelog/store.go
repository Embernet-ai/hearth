package timelog

import (
	"context"
	"time"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Store is what the rest of Hearth calls on this domain. *Service, backed by
// Postgres, is the standalone implementation and is unchanged; with
// HEARTH_BACKEND=emberops the same calls are served from EmberOPS
// (internal/emberops/stores). Callers hold a Store, never a *Service.
type Store interface {
	PersonHours(ctx context.Context, orgID, actorID, personID string, weekFrom, monthFrom, today time.Time) (*PersonHours, error)
	ApproveMany(ctx context.Context, orgID, actorID string, entryIDs []string) (int, error)
	BatchTag(ctx context.Context, orgID string, actor core.Subjects, principalID, tagID string, from, to time.Time) (int, error)
	CanApprove(ctx context.Context, orgID, actorID, subjectID string) (bool, error)
	Correct(ctx context.Context, orgID string, actor core.Subjects,
		entryID string, minutes int, on *time.Time, note string) error
	CreateTag(ctx context.Context, orgID, actorID, name, description string) (*Tag, error)
	Decide(ctx context.Context, orgID, actorID, entryID string,
		approve bool, note string) error
	Decided(ctx context.Context, orgID, actorID string,
		from, to time.Time, seeAll bool) ([]Entry, error)
	DeleteImported(ctx context.Context, orgID, docID, note string) error
	DeleteTag(ctx context.Context, orgID, actorID, tagID string) error
	EffortFor(ctx context.Context, orgID string, actor core.Subjects, docID string) (*Effort, error)
	Entries(ctx context.Context, orgID string, actor core.Subjects, docID string) ([]Entry, error)
	EntryTags(ctx context.Context, orgID, entryID string) ([]Tag, error)
	ForAsset(ctx context.Context, orgID string, actor core.Subjects, assetID string) (*AssetEffort, error)
	Imported(ctx context.Context, orgID, actorID, note string) ([]ImportedEntry, error)
	ListTags(ctx context.Context, orgID string) ([]Tag, error)
	Log(ctx context.Context, orgID string, actor core.Subjects,
		docID string, minutes int, on time.Time, note string) (*Entry, error)
	LogOwn(ctx context.Context, orgID string, actor core.Subjects,
		docID string, minutes int, on time.Time, note string) (*Entry, error)
	Mine(ctx context.Context, orgID, principalID string,
		from, to time.Time, showRemoved bool) ([]Entry, error)
	MineTotal(ctx context.Context, orgID, principalID string,
		from, to time.Time) (total, pending int, err error)
	Pending(ctx context.Context, orgID, actorID string) ([]Entry, error)
	PendingCount(ctx context.Context, orgID, actorID string) (int, error)
	Reassign(ctx context.Context, orgID, actorID, note string, moves []Move) (int, error)
	Remove(ctx context.Context, orgID string, actor core.Subjects, entryID string) error
	// RemoveMany is the admin removal: a super admin names entries and a reason,
	// and apply false reports what would go without changing anything.
	RemoveMany(ctx context.Context, orgID string, actor core.Subjects, ids []string, reason string, apply bool) ([]RemovalLine, error)
	// AutoApproveSince is when a person's time began approving itself, nil while
	// the setting is off. A back end that keeps entries outside Postgres asks it
	// because the setting lives on the person, which stays in Postgres.
	AutoApproveSince(ctx context.Context, orgID, principalID string) (*time.Time, error)
	// AutoApproveMany approves a person's waiting entries by their own auto-approve
	// setting, for time filed before the setting began: the decider is the setting
	// (the person's id and the reason as the note, auto_approved set), never a human.
	// Refused while the setting is off. All or nothing. The audit row names no actor.
	AutoApproveMany(ctx context.Context, orgID, personID string, entryIDs []string, reason string) (int, error)
	// AuditAutoApprove writes the audit row AutoApproveMany writes, for a back end
	// that made the decisions in its own store.
	AuditAutoApprove(ctx context.Context, orgID, personID string, entryIDs []string, reason string) error
	// AuditApproveMany writes the audit row for a bulk approval a back end made
	// itself, the same row ApproveMany writes in Postgres.
	AuditApproveMany(ctx context.Context, orgID, actorID string, entryIDs []string) error
	// AuditRemoval writes the audit row for a removal a back end made itself.
	AuditRemoval(ctx context.Context, orgID, actorID, entryID, via, reason string, before RemovedEntry) error
	Reopen(ctx context.Context, orgID, actorID, entryID string) error
	Restore(ctx context.Context, orgID string, actor core.Subjects, entryID string) error
	Running(ctx context.Context, orgID, principalID string) (*Entry, error)
	SetCapacity(ctx context.Context, orgID, principalID string, minutesPerWeek int, rateCents *int) error
	SetEstimate(ctx context.Context, orgID string, actor core.Subjects, docID string, minutes *int) error
	Start(ctx context.Context, orgID string, actor core.Subjects, docID, note string) (*Entry, error)
	Stop(ctx context.Context, orgID, principalID string) (*Entry, error)
	TagEntry(ctx context.Context, orgID string, actor core.Subjects, entryID, tagID string) error
	TimeReport(ctx context.Context, orgID, actorID, principalID string, from, to time.Time) ([]ReportRow, error)
	TimeReportRows(ctx context.Context, orgID, actorID string, principalIDs []string, from, to time.Time, fn func(ReportRow) error) error
	TimeReportSummary(ctx context.Context, orgID, actorID string, from, to time.Time) ([]ReportTotal, error)
	UntagEntry(ctx context.Context, orgID string, actor core.Subjects, entryID, tagID string) error
	UpdateTag(ctx context.Context, orgID, actorID, tagID, name, description string) error
	Workload(ctx context.Context, orgID, actorID string, from, to time.Time) ([]Load, error)
}

var _ Store = (*Service)(nil)
