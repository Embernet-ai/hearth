package migrate

import (
	"strings"

	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/emberops/mapping"
)

// docTree pages documents of the given kinds parents first. A parent of some
// other kind is not a reference EmberOPS can hold (a Task's parent is a Task),
// so such a document is a root and its parent_id is dropped.
func docTree(kinds, extra string) string {
	return `WITH RECURSIVE tree AS (
		SELECT d.id, 0 AS depth FROM documents d WHERE d.org_id = $3 AND d.kind IN (` + kinds + `)
		  AND (d.parent_id IS NULL OR NOT EXISTS (SELECT 1 FROM documents p WHERE p.id = d.parent_id AND p.kind IN (` + kinds + `)))
		UNION ALL
		SELECT c.id, tree.depth + 1 FROM documents c JOIN tree ON c.parent_id = tree.id
		 WHERE c.kind IN (` + kinds + `) AND tree.depth < 50
	) SELECT lpad(tree.depth::text, 3, '0') || tree.id::text AS cur, tree.id::text AS id, ` + extra
}

// docColumns are the document columns every document step carries.
func docColumns(kinds string) string {
	return `d.kind, d.title, d.body, d.body_format, d.space_id::text AS space_id, d.deleted_at,
		CASE WHEN EXISTS (SELECT 1 FROM documents p WHERE p.id = d.parent_id AND p.kind IN (` + kinds + `))
		     THEN d.parent_id::text END AS parent_id,
		` + checklistSQL + ` AS checklist, ` + propertiesSQL + ` AS properties`
}

func kindCount(kinds string) string {
	return `SELECT count(*) FROM documents WHERE org_id = $1 AND kind IN (` + kinds + `)`
}

func documentSteps(opts Options) []Step {
	const taskLike = `'task'`
	const wo = `'work_order'`
	const prose = `'doc','wiki','note','template','daily_report'`
	return []Step{
		{
			name: "documents", doctypes: []string{"Hearth Document"},
			count: kindCount(prose),
			query: page(docTree(prose, docColumns(prose)+`,
				d.properties_template_kind AS template_kind
				FROM tree JOIN (SELECT dd.*, (SELECT pv.value #>> '{}' FROM property_values pv JOIN property_defs pd ON pd.id = pv.def_id
				   WHERE pv.entity_id = dd.id AND pd.key = 'template_kind') AS properties_template_kind FROM documents dd) d ON d.id = tree.id`)),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Hearth Document", r.S("id"), b.z.DocumentDoc(r)), ""
			},
		},
		{
			name: "canvases", doctypes: []string{"Hearth Document"},
			count: `SELECT count(*) FROM canvases WHERE org_id = $1`,
			query: page(`SELECT c.id::text AS cur, c.id::text AS id, c.name, c.space_id::text AS space_id, c.deleted_at,
				(SELECT COALESCE(jsonb_agg(jsonb_build_object('id', n.id::text, 'entity_type', n.entity_type, 'entity_id', n.entity_id::text,
				   'text', n.text, 'x', n.x, 'y', n.y, 'w', n.w, 'h', n.h, 'style', n.style) ORDER BY n.id), '[]'::jsonb)
				   FROM canvas_nodes n WHERE n.canvas_id = c.id) AS nodes,
				(SELECT COALESCE(jsonb_agg(jsonb_build_object('id', e.id::text, 'from_node', e.from_node::text, 'to_node', e.to_node::text,
				   'label', e.label) ORDER BY e.id), '[]'::jsonb) FROM canvas_edges e WHERE e.canvas_id = c.id) AS edges
				FROM canvases c WHERE c.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Hearth Document", r.S("id"), b.z.CanvasDoc(r, r.List("nodes"), r.List("edges"))), ""
			},
		},
		{
			name: "maintenance plans", doctypes: []string{"Maintenance Plan"},
			count: `SELECT count(*) FROM pm_schedules WHERE org_id = $1`,
			query: page(`SELECT s.id::text AS cur, s.id::text AS id, s.asset_id::text AS asset_id, s.template_id::text AS template_id,
				s.name, s.every_days, s.meter_key, s.every_units, s.lead_days, s.next_due_at, s.last_generated_at,
				s.last_generated_units, s.enabled FROM pm_schedules s WHERE s.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Maintenance Plan", r.S("id"), b.z.PMScheduleDoc(r)), ""
			},
		},
		{
			name: "tasks", doctypes: []string{"Task"},
			count: kindCount(taskLike),
			query: page(docTree(taskLike, docColumns(taskLike)+`,
				tf.status, tf.priority, tf.due_at, tf.started_at, tf.completed_at, tf.estimate_minutes,
				`+assigneesSQL+` AS assignee_emails, `+primaryEmail()+` AS primary_assignee_email
				FROM tree JOIN documents d ON d.id = tree.id LEFT JOIN task_fields tf ON tf.document_id = d.id`)),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Task", r.S("id"), b.z.TaskDoc(r)), ""
			},
		},
		{
			name: "work orders", doctypes: []string{"Maintenance Work Order"},
			count: kindCount(wo),
			query: page(docTree(wo, docColumns(wo)+`,
				tf.status, tf.priority, tf.due_at, tf.started_at, tf.completed_at, tf.estimate_minutes,
				`+assigneesSQL+` AS assignee_emails, `+primaryEmail()+` AS primary_assignee_email,
				(SELECT l.to_id::text FROM entity_links l JOIN documents a ON a.id = l.to_id AND a.kind = 'asset'
				  WHERE l.from_id = d.id AND l.kind = 'concerns' ORDER BY l.created_at LIMIT 1) AS asset_id,
				oc.schedule_id::text AS schedule_id, oc.due_at AS pm_due_at, oc.reason AS pm_reason
				FROM tree JOIN documents d ON d.id = tree.id LEFT JOIN task_fields tf ON tf.document_id = d.id
				LEFT JOIN pm_occurrences oc ON oc.work_order_id = d.id`)),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Maintenance Work Order", r.S("id"), b.z.WorkOrderDoc(r)), ""
			},
		},
		{
			name: "meter readings", doctypes: []string{"Hearth Meter Reading"},
			count: `SELECT count(*) FROM meter_readings WHERE org_id = $1`,
			query: page(`SELECT m.id::text AS cur, m.id::text AS id, m.asset_id::text AS asset_id, m.key, m.value, m.read_at, m.note,
				` + email("m.read_by") + ` AS read_by_email FROM meter_readings m WHERE m.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Hearth Meter Reading", r.S("id"), b.z.MeterReadingDoc(r)), ""
			},
		},
		{
			name: "downtime", doctypes: []string{"Asset Failure Log"},
			count: `SELECT count(*) FROM asset_downtime WHERE org_id = $1`,
			query: page(`SELECT a.id::text AS cur, a.id::text AS id, a.asset_id::text AS asset_id, a.work_order_id::text AS work_order_id,
				a.started_at, a.ended_at, a.minutes, a.planned, a.note FROM asset_downtime a WHERE a.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Asset Failure Log", r.S("id"), b.z.DowntimeDoc(r)), ""
			},
		},
		{
			// Finished entries are Timesheet Detail rows, written one at a time through
			// upsert_time_entry, which files each into its employee's timesheet for the day.
			name: "time entries", doctypes: []string{"Timesheet Detail"},
			count: `SELECT count(*) FROM time_entries WHERE org_id = $1 AND ended_at IS NOT NULL`,
			query: page(`SELECT e.id::text AS cur, e.id::text AS id, e.principal_id::text AS principal_id, e.document_id::text AS document_id,
				d.kind AS target_kind, e.started_at, e.ended_at, e.minutes, e.note, e.deleted_at, e.approved, e.decided_at,
				e.decided_note, ` + email("e.decided_by") + ` AS decided_by_email, ` + email("e.principal_id") + ` AS principal_email,
				(SELECT COALESCE(jsonb_agg(tg.name::text ORDER BY tg.name), '[]'::jsonb) FROM time_entry_tag_links l
				   JOIN time_entry_tags tg ON tg.id = l.tag_id WHERE l.entry_id = e.id) AS tags
				FROM time_entries e LEFT JOIN documents d ON d.id = e.document_id
				WHERE e.org_id = $3 AND e.ended_at IS NOT NULL`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				if !r.Has("principal_email") {
					// A time entry is filed in an Employee's timesheet, and a person with no email address has no User and no
					// Employee. Said here, in the report, rather than as a refusal for a person EmberOps cannot know.
					return nil, "its person has no email address on file, so there is no Employee to file it under"
				}
				return item("Timesheet Detail", r.S("id"), b.z.TimeEntryRow(r, r.Strings("tags"), opts.ActivityType)), ""
			},
		},
		{
			name: "running timers", doctypes: []string{"Hearth Timer"},
			count: `SELECT count(*) FROM time_entries WHERE org_id = $1 AND ended_at IS NULL`,
			query: page(`SELECT e.id::text AS cur, e.id::text AS id, e.principal_id::text AS principal_id, e.document_id::text AS document_id,
				d.kind AS target_kind, e.started_at, e.note FROM time_entries e LEFT JOIN documents d ON d.id = e.document_id
				WHERE e.org_id = $3 AND e.ended_at IS NULL`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Hearth Timer", r.S("id"), b.z.TimerDoc(r)), ""
			},
		},
		{
			name: "stock movements", doctypes: []string{"Stock Entry"},
			count: `SELECT count(*) FROM part_movements WHERE org_id = $1`,
			query: page(`SELECT m.id::text AS cur, m.id::text AS id, m.part_id::text AS part_id, m.document_id::text AS document_id,
				d.kind AS target_kind, m.quantity, m.unit_cost_cents, m.reason, m.note, m.moved_at,
				COALESCE(NULLIF(btrim(p.sku), ''), p.name) AS item_code
				FROM part_movements m JOIN parts p ON p.id = m.part_id LEFT JOIN documents d ON d.id = m.document_id
				WHERE m.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Stock Entry", r.S("id"), b.z.PartMovementDoc(r, r.S("item_code"), opts.Warehouse, opts.CurrencyPerCent())), ""
			},
		},
		{
			name: "purchase orders", doctypes: []string{"Purchase Order"},
			count: kindCount(`'purchase_order'`),
			query: page(docTree(`'purchase_order'`, docColumns(`'purchase_order'`)+`, tf.due_at
				FROM tree JOIN documents d ON d.id = tree.id LEFT JOIN task_fields tf ON tf.document_id = d.id`)),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Purchase Order", r.S("id"), b.z.PurchaseOrderDoc(r)), ""
			},
		},
		{
			name: "calendar events", doctypes: []string{"Event"},
			count: `SELECT count(*) FROM calendar_events WHERE org_id = $1`,
			query: page(`SELECT e.id::text AS cur, e.id::text AS id, e.principal_id::text AS principal_id, e.space_id::text AS space_id,
				e.title, e.notes, e.location, e.starts_at, e.ends_at, e.all_day, ` + email("e.principal_id") + ` AS owner_email
				FROM calendar_events e WHERE e.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Event", r.S("id"), b.z.EventDoc(r)), ""
			},
		},
		{
			name: "saved views", doctypes: []string{"Hearth View"},
			count: `SELECT count(*) FROM saved_views WHERE org_id = $1`,
			query: page(`SELECT v.id::text AS cur, v.id::text AS id, v.space_id::text AS space_id, v.name, v.kind, v.query, v.shared,
				` + email("v.owner_id") + ` AS owner_email FROM saved_views v WHERE v.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				if !r.Has("owner_email") {
					return nil, "its owner has no email address on file"
				}
				return item("Hearth View", r.S("id"), mapping.ViewDoc(r)), ""
			},
		},
		{
			name: "rules", doctypes: []string{"Hearth Rule"},
			count: `SELECT count(*) FROM rules WHERE org_id = $1`,
			query: page(`SELECT r.id::text AS cur, r.id::text AS id, r.space_id::text AS space_id, r.name, r.trigger, r.conditions,
				r.actions, r.enabled FROM rules r WHERE r.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Hearth Rule", r.S("id"), mapping.RuleDoc(r)), ""
			},
		},
		{
			name: "time off", doctypes: []string{"Hearth Time Off Request"},
			count: `SELECT count(*) FROM time_off_requests WHERE org_id = $1`,
			query: page(`SELECT t.id::text AS cur, t.id::text AS id, t.requester_id::text AS requester_id, t.kind, t.status, t.starts_on,
				t.ends_on, t.days, t.reason, t.comments, t.decided_at, t.decision_note, ` + email("t.decided_by") + ` AS decided_by_email
				FROM time_off_requests t WHERE t.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Hearth Time Off Request", r.S("id"), b.z.TimeOffDoc(r)), ""
			},
		},
		{
			name: "department requests", doctypes: []string{"Hearth Department Request"},
			count: `SELECT count(*) FROM department_requests WHERE org_id = $1`,
			query: page(`SELECT r.id::text AS cur, r.id::text AS id, r.requester_id::text AS requester_id, r.status, r.note, r.decided_at,
				r.decision_note, ` + email("r.decided_by") + ` AS decided_by_email,
				(SELECT COALESCE(jsonb_agg(jsonb_build_object('space_id', s.space_id::text, 'kind', s.kind)), '[]'::jsonb)
				   FROM department_request_spaces s WHERE s.request_id = r.id) AS spaces
				FROM department_requests r WHERE r.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				return item("Hearth Department Request", r.S("id"), b.z.DepartmentRequestDoc(r, r.List("spaces"))), ""
			},
		},
		{
			name: "entity links", doctypes: []string{"Hearth Entity Link"},
			count: `SELECT count(*) FROM entity_links WHERE org_id = $1`,
			query: page(`SELECT l.id::text AS cur, l.id::text AS id, l.from_type, l.from_id::text AS from_id, l.to_type, l.to_id::text AS to_id,
				l.kind, fd.kind AS from_kind, td.kind AS to_kind
				FROM entity_links l LEFT JOIN documents fd ON l.from_type = 'document' AND fd.id = l.from_id
				LEFT JOIN documents td ON l.to_type = 'document' AND td.id = l.to_id WHERE l.org_id = $3`),
			build: func(b *builder, r mapping.Rec) ([]emberops.BulkItem, string) {
				from, to := linkDoctype(r.S("from_type"), r.S("from_kind")), linkDoctype(r.S("to_type"), r.S("to_kind"))
				if !opts.CRMParties && (r.S("from_type") == "contact" || r.S("to_type") == "contact") {
					// A contact is a Contact in EmberOps only when the CRM lives there. With the CRM in Hearth the contact is
					// not in EmberOps, so a link to it (143 of Fireball's) can only be refused.
					return nil, "links a contact, which stays in Hearth's CRM"
				}
				if from == "" || to == "" {
					return nil, "links " + r.S("from_type") + " to " + r.S("to_type") + ", which EmberOps does not model"
				}
				return item("Hearth Entity Link",
					mapping.EntityLinkID(r.S("from_type"), r.S("from_id"), r.S("to_type"), r.S("to_id"), r.S("kind")),
					mapping.EntityLinkDoc(r, linkDoctype)), ""
			},
		},
	}
}

func linkDoctype(entityType, kind string) string { return mapping.LinkDoctype(entityType, kind) }

func item(doctype, hearthID string, values mapping.Doc) []emberops.BulkItem {
	delete(values, "doctype")
	delete(values, "hearth_id")
	if doctype == "Timesheet Detail" {
		values["hearth_id"] = strings.ToLower(hearthID) // upsert_time_entry takes it inside values
	}
	return []emberops.BulkItem{{Doctype: doctype, HearthID: hearthID, Values: values}}
}
