package stores

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/docs"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
)

// Property definitions, property values and entity links.
//
// Hearth's views and searches filter and group by property values and walk
// links with SQL, so the values and links stay in Postgres (the spine) and
// every change is copied into EmberOPS as it is made: values ride in the
// document's properties JSON (hearth_properties on Task, Maintenance Work
// Order, Asset and Purchase Order, properties on a Hearth Document), the
// definitions become Hearth Property Defs, and each link a Hearth Entity Link
// whose id is derived from the relationship. A property value edited inside
// EmberOPS is not read back.

var propertyKinds = []string{docs.KindTask, docs.KindWorkOrder, docs.KindAsset, docs.KindDoc, docs.KindWiki,
	docs.KindNote, docs.KindPurchaseOrder, docs.KindDailyReport, docs.KindTemplate}

// defsSent remembers which orgs' definitions this process has already copied
// from an Ensure call, which runs on page loads.
var defsSent sync.Map

// projectDefs copies the property definitions into EmberOPS. applies_to is
// rebuilt from the per-kind lists.
func (s *Docs) projectDefs(ctx context.Context, orgID string) error {
	all, err := s.Store.PropertyDefsFor(ctx, orgID, "")
	if err != nil {
		return err
	}
	applies := map[string][]any{}
	for _, k := range propertyKinds {
		ds, err := s.Store.PropertyDefsFor(ctx, orgID, k)
		if err != nil {
			return err
		}
		for _, d := range ds {
			applies[d.ID] = append(applies[d.ID], k)
		}
	}
	items := make([]emberops.BulkItem, 0, len(all))
	for _, d := range all {
		opts := make([]any, 0, len(d.Options))
		for _, o := range d.Options {
			opts = append(opts, o)
		}
		doc := mapping.PropertyDefDoc(mapping.Rec{"id": d.ID, "key": d.Key, "display_name": d.Label, "data_type": d.DataType,
			"applies_to": applies[d.ID], "options": opts})
		delete(doc, "doctype")
		delete(doc, "hearth_id")
		items = append(items, emberops.BulkItem{Doctype: "Hearth Property Def", HearthID: d.ID, Values: doc})
	}
	if len(items) == 0 {
		return nil
	}
	res, err := s.C.BulkUpsert(ctx, items)
	if err != nil {
		return err
	}
	if len(res.Failed) > 0 {
		return fmt.Errorf("emberops: property definition %s refused: %s", res.Failed[0].HearthID, res.Failed[0].Error)
	}
	return nil
}

func (s *Docs) ensureDefs(ctx context.Context, orgID string) {
	if _, done := defsSent.LoadOrStore(orgID, true); done {
		return
	}
	if err := s.projectDefs(ctx, orgID); err != nil {
		defsSent.Delete(orgID) // try again on the next page
		s.log().Warn("emberops: could not copy property definitions", "error", err)
	}
}

func (s *Docs) EnsureAssetProperties(ctx context.Context, orgID string) error {
	if err := s.Store.EnsureAssetProperties(ctx, orgID); err != nil {
		return err
	}
	s.ensureDefs(ctx, orgID)
	return nil
}

func (s *Docs) EnsureTemplateProperties(ctx context.Context, orgID string) error {
	if err := s.Store.EnsureTemplateProperties(ctx, orgID); err != nil {
		return err
	}
	s.ensureDefs(ctx, orgID)
	return nil
}

func (s *Docs) UpsertImportedProperty(ctx context.Context, orgID string, d docs.ImportedProperty) (string, error) {
	id, err := s.Store.UpsertImportedProperty(ctx, orgID, d)
	if err != nil {
		return "", err
	}
	defsSent.Delete(orgID)
	s.ensureDefs(ctx, orgID)
	return id, nil
}

// SetProperties saves values in the spine and sends the document, whose record
// carries its values.
func (s *Docs) SetProperties(ctx context.Context, orgID string, actor core.Subjects, entityID string, values map[string]string) error {
	if err := s.Store.SetProperties(ctx, orgID, actor, entityID, values); err != nil {
		return err
	}
	return s.pushByID(ctx, orgID, actor, entityID)
}

// ---- links ----

// ends names the two ends of a link for EmberOPS: the doctype, with a document's
// kind read from the spine. It reports false for an end EmberOPS holds nothing
// for.
func (s *Docs) ends(ctx context.Context, orgID string, actor core.Subjects, refs ...core.Ref) ([]string, bool) {
	out := make([]string, len(refs))
	for i, r := range refs {
		kind := ""
		if r.Type == core.EntityDocument {
			d, err := s.Store.Get(ctx, orgID, actor, r.ID)
			if err != nil {
				return nil, false
			}
			kind = d.Kind
		}
		out[i] = mapping.LinkDoctype(string(r.Type), kind)
		if r.Type == core.EntityCompany {
			// A company is a Customer or a Supplier, by its kind.
			for _, dt := range []string{"Customer", "Supplier"} {
				if n, err := s.C.Resolve(ctx, dt, r.ID); err == nil && n != "" {
					out[i] = dt
					break
				}
			}
		}
		if out[i] == "" {
			return nil, false
		}
	}
	return out, true
}

func (s *Docs) pushLink(ctx context.Context, orgID string, actor core.Subjects, from, to core.Ref, kind string) {
	dts, ok := s.ends(ctx, orgID, actor, from, to)
	if !ok {
		return // a file, a channel: EmberOPS holds no such thing
	}
	if kind == "" {
		kind = docs.LinkMention
	}
	id := mapping.EntityLinkID(string(from.Type), from.ID, string(to.Type), to.ID, kind)
	vals := map[string]any{"from_doctype": dts[0], "from_name": emberops.Ref(from.ID),
		"to_doctype": dts[1], "to_name": emberops.Ref(to.ID), "link_kind": kind}
	if _, err := s.C.Upsert(ctx, "Hearth Entity Link", id, vals); err != nil {
		s.log().Warn("emberops: could not copy a link", "from", from.ID, "to", to.ID, "error", err)
	}
}

func (s *Docs) Link(ctx context.Context, orgID string, actor core.Subjects, from, to core.Ref, kind string) error {
	if err := s.Store.Link(ctx, orgID, actor, from, to, kind); err != nil {
		return err
	}
	s.pushLink(ctx, orgID, actor, from, to, kind)
	return nil
}

var linkKinds = []string{docs.LinkMention, docs.LinkConcerns, docs.LinkBlocks, docs.LinkRelates}

// dropLinks deletes the links between two entities in either direction, for the
// given kinds: one list, and a delete for each that is there.
func (s *Docs) dropLinks(ctx context.Context, from, to core.Ref, kinds []string) {
	var ids []any
	for _, k := range kinds {
		ids = append(ids,
			strings.ToLower(mapping.EntityLinkID(string(from.Type), from.ID, string(to.Type), to.ID, k)),
			strings.ToLower(mapping.EntityLinkID(string(to.Type), to.ID, string(from.Type), from.ID, k)))
	}
	rows, err := s.C.List(ctx, emberops.ListQuery{Doctype: "Hearth Entity Link", Fields: []string{"name"}, Limit: 500,
		Filters: []emberops.Filter{{Field: "hearth_id", Op: "in", Value: ids}}})
	if err != nil {
		s.log().Warn("emberops: could not look for links to remove", "error", err)
		return
	}
	for _, r := range rows {
		if err := s.C.Delete(ctx, "Hearth Entity Link", r.S("name")); err != nil {
			s.log().Warn("emberops: could not remove a link", "error", err)
		}
	}
}

func (s *Docs) Unlink(ctx context.Context, orgID string, actor core.Subjects, a, b string) error {
	if err := s.Store.Unlink(ctx, orgID, actor, a, b); err != nil {
		return err
	}
	s.dropLinks(ctx, core.Ref{Type: core.EntityDocument, ID: a}, core.Ref{Type: core.EntityDocument, ID: b}, linkKinds)
	return nil
}

func (s *Docs) UnlinkRefs(ctx context.Context, orgID string, actor core.Subjects, from, to core.Ref, kind string) error {
	if err := s.Store.UnlinkRefs(ctx, orgID, actor, from, to, kind); err != nil {
		return err
	}
	if kind == "" {
		kind = docs.LinkMention
	}
	s.dropLinks(ctx, from, to, []string{kind})
	return nil
}
