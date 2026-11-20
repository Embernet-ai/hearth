package docs

import (
	"context"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Store is what the rest of Hearth calls on this domain. *Service, backed by
// Postgres, is the standalone implementation and is unchanged; with
// HEARTH_BACKEND=emberops the same calls are served from EmberOPS
// (internal/emberops/stores). Callers hold a Store, never a *Service.
type Store interface {
	ApplyExternal(ctx context.Context, orgID, id string, c ExternalChange) error
	ChecklistItemDoc(ctx context.Context, orgID, itemID string) (string, error)
	ExternalMarks(ctx context.Context, ids []string) (map[string]string, error)
	SetExternalMarks(ctx context.Context, marks map[string]string) error
	AddChecklistItem(ctx context.Context, orgID string, actor core.Subjects, docID, text, description string) (*ChecklistItem, error)
	Assign(ctx context.Context, orgID string, actor core.Subjects, docID string, assigneeIDs []string, primary string) error
	Assignable(ctx context.Context, orgID string) ([]People, error)
	AssignedTo(ctx context.Context, docID string) (ids []string, primary string, err error)
	Assignees(ctx context.Context, docID string) ([]string, error)
	Attach(ctx context.Context, orgID string, actor core.Subjects, docID, fileID string) error
	Attachments(ctx context.Context, orgID string, actor core.Subjects, docID string) ([]Attachment, error)
	CanAttach(ctx context.Context, actor core.Subjects, docID string) error
	CanComment(ctx context.Context, actor core.Subjects, docID string) error
	CanReadAttachment(ctx context.Context, orgID string, actor core.Subjects, docID, fileID string) error
	Checklist(ctx context.Context, orgID string, actor core.Subjects, docID string) ([]ChecklistItem, error)
	Children(ctx context.Context, orgID string, actor core.Subjects, parentID string) ([]*Document, error)
	Count(ctx context.Context, orgID string, actor core.Subjects, q Query) (int, error)
	Create(ctx context.Context, orgID string, actor core.Subjects, p CreateParams) (*Document, error)
	CreateAsAgent(ctx context.Context, orgID string, actor core.Subjects,
		p CreateParams) (*Document, error)
	CreateAsRule(ctx context.Context, orgID string, actor RuleActor,
		p CreateParams, linkTo core.Ref) error
	CreateAsSystem(ctx context.Context, orgID string, actor RuleActor,
		p CreateParams, linkTo core.Ref) (*Document, error)
	Delete(ctx context.Context, orgID string, actor core.Subjects, id string) error
	EnsureAssetProperties(ctx context.Context, orgID string) error
	EnsureTemplateProperties(ctx context.Context, orgID string) error
	ExternalRefFor(ctx context.Context, orgID, docID string) (*ExternalRef, error)
	FindByExternalID(ctx context.Context, orgID, source, externalID string) (string, error)
	Get(ctx context.Context, orgID string, actor core.Subjects, id string) (*Document, error)
	GetAsSystem(ctx context.Context, orgID, id string) (*Document, error)
	Guests(ctx context.Context, orgID, docID string) ([]Guest, error)
	History(ctx context.Context, orgID string, actor core.Subjects, docID string, limit int) ([]*Version, error)
	Instantiate(ctx context.Context, orgID string, actor core.Subjects, p InstantiateParams) (*Document, error)
	InstantiateAsSystem(ctx context.Context, orgID string, actor RuleActor,
		p InstantiateParams, linkTo core.Ref) (*Document, error)
	Link(ctx context.Context, orgID string, actor core.Subjects, from, to core.Ref, kind string) error
	Linked(ctx context.Context, orgID string, actor core.Subjects, docID string) ([]*Document, error)
	LinkedTo(ctx context.Context, orgID string, actor core.Subjects,
		ref core.Ref) ([]*Document, error)
	List(ctx context.Context, orgID string, actor core.Subjects, p ListParams) ([]*Document, error)
	MatchesUnfiltered(ctx context.Context, orgID, docID string, q Query) (bool, error)
	MayAssign(ctx context.Context, orgID string, actor core.Subjects, docID string) (bool, error)
	PropertiesFor(ctx context.Context, orgID, kind, entityID string) ([]PropertyDef, error)
	PropertyDefByKey(ctx context.Context, orgID, key string) (*PropertyDef, error)
	PropertyDefsFor(ctx context.Context, orgID, kind string) ([]PropertyDef, error)
	PropertyValuesFor(ctx context.Context, orgID string, docIDs []string) (map[string]map[string]string, error)
	RecordExternalRef(ctx context.Context, orgID string, ref core.Ref, e ExternalRef) error
	RemoveAttachment(ctx context.Context, orgID string, actor core.Subjects, docID, fileID string) error
	RemoveChecklistItem(ctx context.Context, orgID string, actor core.Subjects, itemID string) error
	RemovedAttachments(ctx context.Context, orgID string, actor core.Subjects, docID string) ([]Attachment, error)
	Restore(ctx context.Context, orgID string, actor core.Subjects, docID string, version int64) (*Document, error)
	RestoreAttachment(ctx context.Context, orgID string, actor core.Subjects, docID, fileID string) error
	Search(ctx context.Context, orgID string, actor core.Subjects, q Query) ([]*Row, error)
	SearchWatched(ctx context.Context, orgID string, actor core.Subjects,
		principalID string, q Query) ([]*Row, error)
	SetAssignees(ctx context.Context, docID string, principalIDs []string, primary string) error
	SetChecklistItemDone(ctx context.Context, orgID string, actor core.Subjects, itemID string, done bool) error
	SetField(ctx context.Context, orgID string, actor core.Subjects,
		docID string, field Field, value string) error
	SetFieldAsRule(ctx context.Context, orgID string, actor RuleActor,
		docID string, field Field, value string) error
	SetParent(ctx context.Context, orgID string, actor core.Subjects, id, parentID string) error
	SetProperties(ctx context.Context, orgID string, actor core.Subjects,
		entityID string, values map[string]string) error
	Share(ctx context.Context, orgID string, actor core.Subjects,
		docID string, subject core.Subject, level core.AccessLevel, grantedBy string) error
	ShareWith(ctx context.Context, orgID string, actor core.Subjects, docID, personID string, level core.AccessLevel) error
	Templates(ctx context.Context, orgID string, actor core.Subjects) ([]Template, error)
	Unlink(ctx context.Context, orgID string, actor core.Subjects, a, b string) error
	UnlinkRefs(ctx context.Context, orgID string, actor core.Subjects,
		from, to core.Ref, kind string) error
	Unshare(ctx context.Context, orgID string, actor core.Subjects,
		docID string, subject core.Subject, actorID string) error
	UnshareGuest(ctx context.Context, orgID string, actor core.Subjects, docID, personID string) error
	Update(ctx context.Context, orgID string, actor core.Subjects, id string, p UpdateParams) (*Document, error)
	UpsertImportedProperty(ctx context.Context, orgID string, d ImportedProperty) (string, error)
	DefaultReportSpace(ctx context.Context, orgID, principalID string) (string, error)
	EnsureReportSpace(ctx context.Context, orgID, creatorID string) (string, error)
	Reassign(ctx context.Context, orgID, actorID string, plan []Reassignment, apply, restore bool) (*ReassignReport, error)
	ReportOn(ctx context.Context, orgID, principalID, day string) (string, error)
	Reports(ctx context.Context, orgID string, actor core.Subjects, f ReportFilter) ([]*ReportEntry, error)
	ServiceHistory(ctx context.Context, orgID string, actor core.Subjects, f ServiceFilter) ([]*ServiceRecord, error)
	VersionAt(ctx context.Context, orgID string, actor core.Subjects, docID string, version int64) (*Version, error)
}

var _ Store = (*Service)(nil)
