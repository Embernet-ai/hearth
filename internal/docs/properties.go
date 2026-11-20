package docs

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Property data types, matching the CHECK constraint in the substrate.
const (
	PropText   = "text"
	PropNumber = "number"
	PropDate   = "date"
	PropSelect = "select"
	PropURL    = "url"
)

// PropertyDef is a custom field: what it is called, what shape it holds, and
// which kinds of thing it applies to.
type PropertyDef struct {
	ID       string
	Key      string
	Label    string
	DataType string
	Options  []string
	// Value is the current value for whatever entity was asked about, rendered
	// as a string for the form. Empty when unset.
	Value string
}

// PropertiesFor returns the definitions that apply to a kind, each carrying the
// value currently set on this entity.
//
// Definitions and values together in one query, because a form needs both and
// asking twice means a field that exists with no value renders differently
// depending on which call was made first.
func (s *Service) PropertiesFor(ctx context.Context, orgID, kind, entityID string) ([]PropertyDef, error) {
	rows, err := s.db.Query(ctx, `
		SELECT d.id::text, d.key::text, d.display_name, d.data_type, d.options, v.value
		FROM property_defs d
		LEFT JOIN property_values v
		       ON v.def_id = d.id AND v.entity_type = 'document' AND v.entity_id = $2
		WHERE d.org_id = $1 AND $3 = ANY(d.applies_to)
		ORDER BY d.display_name`, orgID, entityID, kind)
	if err != nil {
		return nil, fmt.Errorf("docs: properties for %s: %w", kind, err)
	}
	defer rows.Close()

	var out []PropertyDef
	for rows.Next() {
		var p PropertyDef
		var rawOptions, rawValue []byte
		if err := rows.Scan(&p.ID, &p.Key, &p.Label, &p.DataType, &rawOptions, &rawValue); err != nil {
			return nil, fmt.Errorf("docs: scan property: %w", err)
		}
		if len(rawOptions) > 0 {
			_ = json.Unmarshal(rawOptions, &p.Options)
		}
		p.Value = renderValue(rawValue)
		out = append(out, p)
	}
	return out, rows.Err()
}

// PropertyValuesFor loads the property values for a whole page of documents at
// once, keyed by document id and then by property key.
//
// One query for the page rather than one per row. A table showing fifty rows
// with two custom columns is a hundred extra round trips done the obvious way,
// and that is the difference between a list that renders and a list people
// complain about.
func (s *Service) PropertyValuesFor(ctx context.Context, orgID string, docIDs []string) (map[string]map[string]string, error) {
	out := make(map[string]map[string]string, len(docIDs))
	if len(docIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT v.entity_id::text, d.key::text, v.value
		FROM property_values v
		JOIN property_defs d ON d.id = v.def_id
		WHERE v.entity_type = 'document'
		  AND v.entity_id = ANY($1::uuid[])
		  AND d.org_id = $2`, docIDs, orgID)
	if err != nil {
		return nil, fmt.Errorf("docs: property values for page: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, key string
		var raw []byte
		if err := rows.Scan(&id, &key, &raw); err != nil {
			return nil, fmt.Errorf("docs: scan property value: %w", err)
		}
		if out[id] == nil {
			out[id] = map[string]string{}
		}
		out[id][key] = renderValue(raw)
	}
	return out, rows.Err()
}

// renderValue turns the stored JSON back into something a form input can hold.
// A JSON string is unquoted; anything else is shown as written.
func renderValue(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.Trim(string(raw), `"`)
}

// SetProperties writes the values a form submitted.
//
// Requires edit on the entity, checked once rather than per field. An empty
// value deletes the row instead of storing an empty string, so "not set" has
// exactly one representation and filters do not have to know about two.
func (s *Service) SetProperties(ctx context.Context, orgID string, actor core.Subjects,
	entityID string, values map[string]string) error {

	ref := core.Ref{Type: core.EntityDocument, ID: entityID}
	if err := s.authz.Require(ctx, actor, ref, core.AccessEdit); err != nil {
		return err
	}
	principal := actor.Principal()

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("docs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for defID, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			if _, err := tx.Exec(ctx, `
				DELETE FROM property_values
				WHERE entity_type='document' AND entity_id=$1 AND def_id=$2`,
				entityID, defID); err != nil {
				return fmt.Errorf("docs: clear property: %w", err)
			}
			continue
		}

		var dataType string
		if err := tx.QueryRow(ctx,
			`SELECT data_type FROM property_defs WHERE id=$1 AND org_id=$2`,
			defID, orgID).Scan(&dataType); err != nil {
			// A field that is not ours to write is skipped rather than failing
			// the whole save, since it can only come from a stale form.
			continue
		}

		encoded, err := encodeValue(dataType, raw)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO property_values (entity_type, entity_id, def_id, value, updated_by)
			VALUES ('document', $1, $2, $3, $4)
			ON CONFLICT (entity_type, entity_id, def_id)
			DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
			entityID, defID, encoded, principal); err != nil {
			return fmt.Errorf("docs: set property: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// encodeValue stores a number as a JSON number rather than a quoted string, so
// that ordering and comparison work in the database instead of sorting 10 before 9.
func encodeValue(dataType, raw string) ([]byte, error) {
	if dataType == PropNumber {
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("docs: %q is not a number", raw)
		}
		return json.Marshal(n)
	}
	return json.Marshal(raw)
}

// defaultAssetProperties is the starting set for assets.
//
// Seeded rather than hardcoded as columns, which is the entire point of the
// property system: a site that tracks a different thing adds it without a
// migration. These four are the ones every maintenance record needs before it
// can be looked up by anything other than its name.
var defaultAssetProperties = []struct {
	Key, Label, DataType string
	Options              []string
}{
	{"asset_tag", "Asset tag", PropText, nil},
	{"location", "Location", PropText, nil},
	{"serial", "Serial number", PropText, nil},
	{"criticality", "Criticality", PropSelect, []string{"low", "medium", "high", "safety"}},
}

// EnsureAssetProperties creates the default asset fields for an org once.
//
// Idempotent, and safe to call on every page load: the unique key on
// (org_id, key) is what makes the second call a no-op rather than a duplicate.
// It runs lazily because orgs created before this existed would otherwise have
// no fields at all and no way to get them.
func (s *Service) EnsureAssetProperties(ctx context.Context, orgID string) error {
	for _, d := range defaultAssetProperties {
		options, err := json.Marshal(d.Options)
		if err != nil {
			return err
		}
		if d.Options == nil {
			options = []byte(`[]`)
		}
		if _, err := s.db.Exec(ctx, `
			INSERT INTO property_defs (id, org_id, key, display_name, data_type, applies_to, options)
			VALUES (gen_random_uuid(), $1, $2, $3, $4, ARRAY['asset'], $5)
			ON CONFLICT (org_id, key) DO NOTHING`,
			orgID, d.Key, d.Label, d.DataType, options); err != nil {
			return fmt.Errorf("docs: seed asset properties: %w", err)
		}
	}
	return nil
}

// ImportedProperty is a property definition brought in from another system.
//
// Separate from PropertyDef because an import knows different things: it has
// the source's own id and no key, where a hand-made definition has a key
// somebody chose and no source.
type ImportedProperty struct {
	Label      string
	DataType   string
	Options    []string
	Source     string
	ExternalID string
}

// UpsertImportedProperty registers a definition from an import and returns its
// id.
//
// Matched on (source, external_id), never on label. Two systems both calling a
// field "Status" are two different fields, and merging them puts one team's
// values in another team's column. It is also why a re-import updates its own
// definitions and can never adopt one somebody created here by hand: those
// carry no source at all.
//
// The key is derived from the label because the table requires one and an
// import has nothing better. It is only ever seen in a form field name.
func (s *Service) UpsertImportedProperty(ctx context.Context, orgID string, d ImportedProperty) (string, error) {
	label := strings.TrimSpace(d.Label)
	if label == "" {
		return "", fmt.Errorf("docs: an imported property needs a label")
	}
	if d.Source == "" || d.ExternalID == "" {
		return "", fmt.Errorf("docs: an imported property needs a source and an external id")
	}
	options, err := json.Marshal(d.Options)
	if err != nil || d.Options == nil {
		options = []byte(`[]`)
	}

	// The key must be unique per org, and two ClickUp spaces can each define
	// "Impact Level". Suffixing with the external id keeps them apart without
	// making the label ugly, which is the part a person reads.
	key := propertyKey(label) + "-" + d.ExternalID

	var id string
	err = s.db.QueryRow(ctx, `
		INSERT INTO property_defs (id, org_id, key, display_name, data_type,
		                           applies_to, options, source, external_id)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, ARRAY['task','work_order','doc','asset'], $5, $6, $7)
		ON CONFLICT (org_id, source, external_id) WHERE source IS NOT NULL AND external_id IS NOT NULL
		DO UPDATE SET display_name = EXCLUDED.display_name,
		              data_type    = EXCLUDED.data_type,
		              options      = EXCLUDED.options,
		              updated_at   = now()
		RETURNING id::text`,
		orgID, key, label, d.DataType, options, d.Source, d.ExternalID,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("docs: upsert imported property: %w", err)
	}
	return id, nil
}

// propertyKey turns a label into something usable as a key: lowercase, and
// anything that is not a letter or digit becomes a hyphen.
func propertyKey(label string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(label) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
