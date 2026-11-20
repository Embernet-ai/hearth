package migrate

import (
	"fmt"

	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
)

// Step copies one Hearth table (or join) into one or more doctypes.
//
// query is keyset-paged: it must expose a text column `cur` that orders the
// rows, take $1 (rows after this cursor), $2 (page size) and $3 (the org id),
// and return `cur` plus the row as jsonb. build turns one row into the bulk
// items it becomes; returning none skips the row, with a reason that lands in
// the report.
type Step struct {
	name     string
	doctypes []string // for the remote count; first is the primary
	query    string
	count    string // SELECT count(*) of the rows this step reads, with $1 = org id
	build    func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string)
}

// email is the SQL for a principal's login email, from local credentials or
// the identity provider's last-seen address.
func email(col string) string {
	return fmt.Sprintf(`COALESCE((SELECT lc.email::text FROM local_credentials lc WHERE lc.principal_id = %[1]s),
	  (SELECT ui.email::text FROM user_identities ui WHERE ui.principal_id = %[1]s AND ui.email <> '' ORDER BY ui.updated_at DESC LIMIT 1))`, col)
}

// page wraps a SELECT that yields `cur` and the columns of the row.
func page(inner string) string {
	return `SELECT cur, to_jsonb(t) FROM (` + inner + `) t WHERE cur > $1 ORDER BY cur LIMIT $2`
}

// checklist and properties are shared sub-selects for anything that is a document.
const (
	checklistSQL = `(SELECT COALESCE(jsonb_agg(jsonb_build_object(
		'id', ci.id::text, 'text', ci.text, 'description', ci.description, 'position', ci.position,
		'done_at', ci.done_at, 'done_by_email', ` + `COALESCE((SELECT lc.email::text FROM local_credentials lc WHERE lc.principal_id = ci.done_by),
		  (SELECT ui.email::text FROM user_identities ui WHERE ui.principal_id = ci.done_by AND ui.email <> '' LIMIT 1))` + `
	) ORDER BY ci.position), '[]'::jsonb) FROM checklist_items ci WHERE ci.document_id = d.id)`

	propertiesSQL = `(SELECT COALESCE(jsonb_object_agg(pd.key::text, pv.value), '{}'::jsonb)
		FROM property_values pv JOIN property_defs pd ON pd.id = pv.def_id
		WHERE pv.entity_id = d.id)`

	assigneesSQL = `(SELECT COALESCE(jsonb_agg(` + `COALESCE((SELECT lc.email::text FROM local_credentials lc WHERE lc.principal_id = da.principal_id),
		  (SELECT ui.email::text FROM user_identities ui WHERE ui.principal_id = da.principal_id AND ui.email <> '' LIMIT 1))` + `), '[]'::jsonb)
		FROM document_assignees da WHERE da.document_id = d.id)`
)

// primaryEmail: the primary assignee, else the task's single assignee.
func primaryEmail() string {
	return `COALESCE(
	  (SELECT ` + email("da.principal_id") + ` FROM document_assignees da WHERE da.document_id = d.id AND da.is_primary LIMIT 1),
	  (SELECT ` + email("tf.assignee_id") + `))`
}

// byDepth orders a self-referencing table parents first. idCol is the key and
// parentCol the self reference; where limits the roots (and so the tree) to an
// org. The cursor is the depth, zero padded, then the id, so paging is stable
// and a child never arrives before its parent. extra is the rest of the SELECT
// list followed by its FROM clause, which starts from tree.
func byDepth(table, idCol, parentCol, extra, where string) string {
	return `WITH RECURSIVE tree AS (
		SELECT ` + idCol + ` AS id, 0 AS depth FROM ` + table + ` WHERE ` + parentCol + ` IS NULL AND ` + where + `
		UNION ALL
		SELECT c.` + idCol + `, tree.depth + 1 FROM ` + table + ` c JOIN tree ON c.` + parentCol + ` = tree.id WHERE tree.depth < 50
	) SELECT lpad(tree.depth::text, 3, '0') || tree.id::text AS cur, tree.id::text AS id, ` + extra
}

func steps(opts Options) []Step {
	return []Step{
		{
			name: "users", doctypes: []string{"User"},
			count: `SELECT count(*) FROM principals WHERE org_id = $1`,
			query: page(`SELECT p.id::text AS cur, p.id::text AS id, p.display_name, p.kind, p.handle::text AS handle, p.status,
				` + email("p.id") + ` AS email FROM principals p WHERE p.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				if !r.Has("email") {
					return nil, "no email address on file, so there is no User to make"
				}
				return item("User", r.S("id"), mapping.UserDoc(r)), ""
			},
		},
		{
			name: "employees", doctypes: []string{"Employee"},
			count: `SELECT count(*) FROM principals WHERE org_id = $1 AND kind = 'user'`,
			query: page(`SELECT p.id::text AS cur, p.id::text AS id, p.display_name, p.handle::text AS handle, p.status,
				` + email("p.id") + ` AS email, c.minutes_per_week
				FROM principals p LEFT JOIN capacity c ON c.principal_id = p.id WHERE p.org_id = $3 AND p.kind = 'user'`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				if !r.Has("email") {
					return nil, "no email address on file, so there is no User to attach an Employee to"
				}
				return item("Employee", r.S("id"), mapping.EmployeeDoc(r)), ""
			},
		},
		{
			// A second pass, so an approver is an Employee by the time they are referenced.
			name: "employee approvers", doctypes: []string{"Employee"},
			count: `SELECT count(*) FROM principals WHERE org_id = $1 AND kind = 'user' AND approver_id IS NOT NULL`,
			query: page(`SELECT p.id::text AS cur, p.id::text AS id, p.approver_id::text AS approver_id
				FROM principals p WHERE p.org_id = $3 AND p.kind = 'user' AND p.approver_id IS NOT NULL`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Employee", r.S("id"), mapping.Doc{"hearth_approver": mapping.Ref(r.S("approver_id"))}), ""
			},
		},
		{
			name: "teams", doctypes: []string{"Employee Group"},
			count: `SELECT count(*) FROM teams WHERE org_id = $1`,
			query: page(`SELECT t.id::text AS cur, t.id::text AS id, t.name, t.external_id, t.source,
				(SELECT COALESCE(jsonb_agg(m.principal_id::text), '[]'::jsonb) FROM team_members m WHERE m.team_id = t.id) AS member_ids
				FROM teams t WHERE t.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Employee Group", r.S("id"), mapping.TeamDoc(r, r.Strings("member_ids"))), ""
			},
		},
		{
			name: "spaces", doctypes: []string{"Hearth Space"},
			count: `SELECT count(*) FROM spaces WHERE org_id = $1`,
			query: page(byDepth("spaces", "id", "parent_id", `x.parent_id::text AS parent_id, x.name, x.kind, x.position,
				(SELECT COALESCE(jsonb_agg(`+email("m.principal_id")+`) FILTER (WHERE `+email("m.principal_id")+` IS NOT NULL), '[]'::jsonb)
				   FROM space_managers m WHERE m.space_id = x.id) AS manager_emails,
				(SELECT COALESCE(jsonb_agg(`+email("d.principal_id")+`) FILTER (WHERE `+email("d.principal_id")+` IS NOT NULL), '[]'::jsonb)
				   FROM principal_departments d WHERE d.space_id = x.id) AS member_emails,
				(SELECT COALESCE(jsonb_agg(jsonb_build_object(
				        'kind', split_part(g.subject_key, ':', 1), 'team_id', split_part(g.subject_key, ':', 2),
				        'email', CASE WHEN split_part(g.subject_key, ':', 1) = 'principal' THEN `+email("split_part(g.subject_key, ':', 2)::uuid")+` END,
				        'level', g.level, 'expires_at', g.expires_at)), '[]'::jsonb)
				   FROM grants g WHERE g.entity_type = 'space' AND g.entity_id = x.id
				    AND (g.expires_at IS NULL OR g.expires_at > now())) AS grants
				FROM tree JOIN spaces x ON x.id = tree.id`, "org_id = $3")),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Hearth Space", r.S("id"), b.z.SpaceAccessDoc(r, r.Strings("manager_emails"), r.Strings("member_emails"),
					r.List("grants"), !r.Has("parent_id"))), ""
			},
		},
		{
			name: "time tags", doctypes: []string{"Activity Type"},
			count: `SELECT count(*) FROM time_entry_tags WHERE org_id = $1`,
			query: page(`SELECT t.id::text AS cur, t.id::text AS id, t.name::text AS name, t.description
				FROM time_entry_tags t WHERE t.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Activity Type", r.S("id"), mapping.TimeTagDoc(r)), ""
			},
		},
		{
			name: "property definitions", doctypes: []string{"Hearth Property Def"},
			count: `SELECT count(*) FROM property_defs WHERE org_id = $1`,
			query: page(`SELECT p.id::text AS cur, p.id::text AS id, p.key::text AS key, p.display_name, p.data_type, p.is_multi,
				to_jsonb(p.applies_to) AS applies_to, p.options, p.external_id FROM property_defs p WHERE p.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Hearth Property Def", r.S("id"), mapping.PropertyDefDoc(r)), ""
			},
		},
		{
			name: "companies", doctypes: []string{"Customer", "Supplier"},
			count: `SELECT count(*) FROM companies WHERE org_id = $1`,
			query: page(`SELECT c.id::text AS cur, c.id::text AS id, c.name, c.domain, c.kind, c.notes, c.deleted_at
				FROM companies c WHERE c.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				d := b.z.CompanyDoc(r)
				return item(d["doctype"].(string), r.S("id"), d), ""
			},
		},
		{
			name: "contacts", doctypes: []string{"Contact"},
			count: `SELECT count(*) FROM contacts WHERE org_id = $1`,
			query: page(`SELECT c.id::text AS cur, c.id::text AS id, c.name, c.email, c.phone, c.title, c.notes, c.deleted_at,
				c.company_id::text AS company_id, co.kind AS company_kind
				FROM contacts c LEFT JOIN companies co ON co.id = c.company_id WHERE c.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Contact", r.S("id"), b.z.ContactDoc(r)), ""
			},
		},
		{
			name: "parts", doctypes: []string{"Item"},
			count: `SELECT count(*) FROM parts WHERE org_id = $1`,
			query: page(`SELECT p.id::text AS cur, p.id::text AS id, p.sku, p.name, p.unit_cost_cents, p.supplier_id::text AS supplier_id,
				p.reorder_at, p.deleted_at FROM parts p WHERE p.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Item", r.S("id"), b.z.PartDoc(r, opts.CurrencyPerCent())), ""
			},
		},
		{
			// Every asset document, deleted ones too, and ones with no asset_fields row: a deleted asset still has plans, work
			// orders, meter readings and downtime pointing at it, and a record that points at nothing is a refused record.
			// An asset's parent that is not itself an asset makes it a root, so no row is lost to the tree.
			name: "assets", doctypes: []string{"Asset"},
			count: `SELECT count(*) FROM documents WHERE org_id = $1 AND kind = 'asset'`,
			query: page(`WITH RECURSIVE a AS (
					SELECT d.id, CASE WHEN af.parent_asset_id IN (SELECT id FROM documents WHERE kind = 'asset') THEN af.parent_asset_id END AS parent_id
					FROM documents d LEFT JOIN asset_fields af ON af.document_id = d.id WHERE d.kind = 'asset' AND d.org_id = $3),
				tree AS (
					SELECT id, 0 AS depth FROM a WHERE parent_id IS NULL
					UNION ALL SELECT c.id, tree.depth + 1 FROM a c JOIN tree ON c.parent_id = tree.id WHERE tree.depth < 50)
				SELECT lpad(tree.depth::text, 3, '0') || tree.id::text AS cur, tree.id::text AS id, d.title, d.body, d.body_format,
					d.space_id::text AS space_id, d.deleted_at, af.code, a.parent_id::text AS parent_asset_id, af.installed_at, af.retired_at,
					` + checklistSQL + ` AS checklist, ` + propertiesSQL + ` AS properties
				FROM tree JOIN a ON a.id = tree.id JOIN documents d ON d.id = tree.id LEFT JOIN asset_fields af ON af.document_id = d.id`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Asset", r.S("id"), b.z.AssetDoc(r)), ""
			},
		},
	}
}
