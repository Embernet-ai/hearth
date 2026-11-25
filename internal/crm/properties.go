package crm

// Custom properties on companies, contacts and deals: the fields a team adds for what it tracks (industry, contract end, line
// speed) without a migration.
//
// They use the property tables the rest of Hearth already has (property_defs and property_values), with the record's kind in
// applies_to and entity_type. A key is stored as crm_<kind>_<key>, so a CRM field never collides with a document property of the
// same name, and shown without the prefix. An admin (a super admin) defines them; anybody who can edit a record sets its values.
// They stay in Hearth: the EmberOps sync never sends or reads them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Embernet-ai/hearth/internal/core"
)

// Property types a CRM field may have.
const (
	PropText     = "text"
	PropNumber   = "number"
	PropDate     = "date"
	PropSelect   = "select"
	PropBoolean  = "boolean"
	PropURL      = "url"
	PropEmail    = "email"
	PropPhone    = "phone"
	PropCurrency = "currency"
)

// PropertyTypes is the list a form offers, in order.
var PropertyTypes = []string{PropText, PropNumber, PropCurrency, PropDate, PropBoolean, PropSelect, PropURL, PropEmail, PropPhone}

// PropertyDef is one custom field.
type PropertyDef struct {
	ID       string
	Kind     core.EntityType
	Key      string
	Label    string
	DataType string
	Options  []string
}

// PropertyValue is a field and what a record holds in it. Value is typed (a string, a number, or a bool) and nil when unset;
// Text is the same as the form shows it.
type PropertyValue struct {
	PropertyDef
	Value any
	Text  string
}

var propKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

func storedKey(kind core.EntityType, key string) string { return "crm_" + string(kind) + "_" + key }

func shownKey(kind core.EntityType, stored string) string {
	return strings.TrimPrefix(stored, "crm_"+string(kind)+"_")
}

func validPropertyType(t string) bool { return contains(PropertyTypes, t) }

// PropertyDefs lists the custom fields of a kind, by label.
func (s *Service) PropertyDefs(ctx context.Context, orgID string, kind core.EntityType) ([]PropertyDef, error) {
	if !validTarget(kind) {
		return nil, fmt.Errorf("%w: custom fields are for companies, contacts, and deals", ErrInvalid)
	}
	rows, err := s.db.Query(ctx, `SELECT id::text, key::text, display_name, data_type, options FROM property_defs
		WHERE org_id=$1 AND $2 = ANY(applies_to) AND key::text LIKE $3 ORDER BY lower(display_name), key`,
		orgID, string(kind), "crm\\_"+string(kind)+"\\_%")
	if err != nil {
		return nil, fmt.Errorf("crm: custom fields: %w", err)
	}
	defer rows.Close()
	var out []PropertyDef
	for rows.Next() {
		var d PropertyDef
		var raw []byte
		if err := rows.Scan(&d.ID, &d.Key, &d.Label, &d.DataType, &raw); err != nil {
			return nil, err
		}
		d.Kind, d.Key = kind, shownKey(kind, d.Key)
		_ = json.Unmarshal(raw, &d.Options)
		out = append(out, d)
	}
	return out, rows.Err()
}

// PropertyInput is a field to make or change. A change keeps the key and the type (a value stored under one type is not
// reinterpreted as another); the label and the options of a select may change.
type PropertyInput struct {
	ID       string // empty makes a new one
	Kind     core.EntityType
	Key      string // made from the label when empty
	Label    string
	DataType string
	Options  []string
}

func requireAdmin(actor core.Subjects) error {
	if !actor.Super() {
		return fmt.Errorf("%w: only an admin defines custom fields", ErrForbidden)
	}
	return nil
}

// ErrForbidden is a change the caller is not allowed to make, as opposed to a record they cannot see.
var ErrForbidden = errors.New("not allowed")

// SaveProperty makes or changes a custom field. Only a super admin may.
func (s *Service) SaveProperty(ctx context.Context, orgID string, actor core.Subjects, in PropertyInput) (*PropertyDef, error) {
	if err := requireAdmin(actor); err != nil {
		return nil, err
	}
	if !validTarget(in.Kind) {
		return nil, fmt.Errorf("%w: custom fields are for companies, contacts, and deals", ErrInvalid)
	}
	in.Label = strings.TrimSpace(in.Label)
	if in.Label == "" || len(in.Label) > 80 {
		return nil, fmt.Errorf("%w: a field needs a label of at most 80 characters", ErrInvalid)
	}
	var opts []string
	for _, o := range in.Options {
		if o = strings.TrimSpace(o); o != "" && len(o) <= 80 && !contains(opts, o) {
			opts = append(opts, o)
		}
	}
	if opts == nil {
		opts = []string{}
	}
	raw, _ := json.Marshal(opts)
	if in.ID != "" {
		var dt, key string
		err := s.db.QueryRow(ctx, `SELECT data_type, key::text FROM property_defs WHERE id=$1 AND org_id=$2`, in.ID, orgID).Scan(&dt, &key)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !strings.HasPrefix(key, "crm_"+string(in.Kind)+"_")) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("crm: find field: %w", err)
		}
		if dt == PropSelect && len(opts) == 0 {
			return nil, fmt.Errorf("%w: a choice field needs choices", ErrInvalid)
		}
		if dt != PropSelect {
			raw = []byte("[]")
		}
		if _, err := s.db.Exec(ctx, `UPDATE property_defs SET display_name=$3, options=$4, updated_at=now() WHERE id=$1 AND org_id=$2`, in.ID, orgID, in.Label, raw); err != nil {
			return nil, fmt.Errorf("crm: change field: %w", err)
		}
		defs, _ := s.PropertyDefs(ctx, orgID, in.Kind)
		for _, d := range defs {
			if d.ID == in.ID {
				return &d, nil
			}
		}
		return nil, ErrNotFound
	}
	if !validPropertyType(in.DataType) {
		return nil, fmt.Errorf("%w: %q is not a kind of field", ErrInvalid, in.DataType)
	}
	if in.DataType == PropSelect && len(opts) == 0 {
		return nil, fmt.Errorf("%w: a choice field needs choices", ErrInvalid)
	}
	if in.DataType != PropSelect {
		raw = []byte("[]")
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		key = KeyFor(in.Label)
	}
	if !propKeyRe.MatchString(key) {
		return nil, fmt.Errorf("%w: a key is lower case letters, digits and underscores, starting with a letter", ErrInvalid)
	}
	id := uuid.NewString()
	_, err := s.db.Exec(ctx, `INSERT INTO property_defs (id, org_id, key, display_name, data_type, is_multi, applies_to, options)
		VALUES ($1,$2,$3,$4,$5,false,ARRAY[$6]::text[],$7)`, id, orgID, storedKey(in.Kind, key), in.Label, in.DataType, string(in.Kind), raw)
	if err != nil {
		if strings.Contains(err.Error(), "property_defs_org_id_key_key") {
			return nil, fmt.Errorf("%w: there is already a field with the key %q", ErrDuplicate, key)
		}
		return nil, fmt.Errorf("crm: make field: %w", err)
	}
	return &PropertyDef{ID: id, Kind: in.Kind, Key: key, Label: in.Label, DataType: in.DataType, Options: opts}, nil
}

// DeleteProperty removes a custom field and every value it holds. Only a super admin may.
func (s *Service) DeleteProperty(ctx context.Context, orgID string, actor core.Subjects, kind core.EntityType, id string) error {
	if err := requireAdmin(actor); err != nil {
		return err
	}
	ct, err := s.db.Exec(ctx, `DELETE FROM property_defs WHERE id=$1 AND org_id=$2 AND key::text LIKE $3`, id, orgID, "crm\\_"+string(kind)+"\\_%")
	if err != nil {
		return fmt.Errorf("crm: delete field: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func textOf(raw []byte) (any, string) {
	if len(raw) == 0 {
		return nil, ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil, ""
	}
	switch t := v.(type) {
	case string:
		return t, t
	case float64:
		return t, strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return t, strconv.FormatBool(t)
	}
	return nil, ""
}

// PropertiesFor is every custom field of the record's kind with what the record holds in it. The caller must be able to see it.
func (s *Service) PropertiesFor(ctx context.Context, orgID string, actor core.Subjects, ref core.Ref) ([]PropertyValue, error) {
	if err := s.authz.Require(ctx, actor, ref, core.AccessView); err != nil {
		return nil, ErrNotFound
	}
	defs, err := s.PropertyDefs(ctx, orgID, ref.Type)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT def_id::text, value FROM property_values WHERE entity_type=$1 AND entity_id=$2`, string(ref.Type), ref.ID)
	if err != nil {
		return nil, fmt.Errorf("crm: field values: %w", err)
	}
	defer rows.Close()
	raws := map[string][]byte{}
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		raws[id] = raw
	}
	out := make([]PropertyValue, 0, len(defs))
	for _, d := range defs {
		v, txt := textOf(raws[d.ID])
		out = append(out, PropertyValue{PropertyDef: d, Value: v, Text: txt})
	}
	return out, rows.Err()
}

// encodeProperty checks a value against its field's type and returns what is stored. An empty value is "not set" and is nil.
func encodeProperty(d PropertyDef, in any) (any, error) {
	if in == nil {
		return nil, nil
	}
	str := func() (string, error) {
		switch t := in.(type) {
		case string:
			return strings.TrimSpace(t), nil
		case float64:
			return strconv.FormatFloat(t, 'f', -1, 64), nil
		case bool:
			return strconv.FormatBool(t), nil
		}
		return "", fmt.Errorf("%w: %s is not text", ErrInvalid, d.Label)
	}
	s, err := str()
	if err != nil {
		return nil, err
	}
	if s == "" {
		return nil, nil
	}
	bad := func(what string) (any, error) { return nil, fmt.Errorf("%w: %s must be %s", ErrInvalid, d.Label, what) }
	switch d.DataType {
	case PropText:
		if len(s) > 2000 {
			return bad("at most 2000 characters")
		}
		return s, nil
	case PropNumber, PropCurrency:
		f, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimPrefix(s, "$"), ",", ""), 64)
		if err != nil {
			return bad("a number")
		}
		return f, nil
	case PropDate:
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return bad("a date, YYYY-MM-DD")
		}
		return s, nil
	case PropBoolean:
		switch strings.ToLower(s) {
		case "true", "yes", "on", "1":
			return true, nil
		case "false", "no", "off", "0":
			return false, nil
		}
		return bad("yes or no")
	case PropSelect:
		for _, o := range d.Options {
			if strings.EqualFold(o, s) {
				return o, nil
			}
		}
		return bad("one of its choices")
	case PropURL:
		if u, err := url.Parse(s); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return bad("a web address starting http:// or https://")
		}
		return s, nil
	case PropEmail:
		if _, err := mail.ParseAddress(s); err != nil {
			return bad("an email address")
		}
		return strings.ToLower(s), nil
	case PropPhone:
		if len(s) > 40 {
			return bad("a phone number")
		}
		return s, nil
	}
	return bad("a value")
}

// SetProperties sets custom field values on a record, by key. A nil or empty value clears the field. Every value is checked
// before any is written, so a bad one changes nothing. The caller needs edit on the record.
func (s *Service) SetProperties(ctx context.Context, orgID string, actor core.Subjects, ref core.Ref, values map[string]any) error {
	if len(values) == 0 {
		return nil
	}
	if err := s.authz.Require(ctx, actor, ref, core.AccessEdit); err != nil {
		return err
	}
	defs, err := s.PropertyDefs(ctx, orgID, ref.Type)
	if err != nil {
		return err
	}
	byKey := map[string]PropertyDef{}
	for _, d := range defs {
		byKey[d.Key] = d
	}
	type write struct {
		def PropertyDef
		val any
	}
	var writes []write
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		d, ok := byKey[k]
		if !ok {
			return fmt.Errorf("%w: there is no custom field %q", ErrInvalid, k)
		}
		v, err := encodeProperty(d, values[k])
		if err != nil {
			return err
		}
		writes = append(writes, write{d, v})
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("crm: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, w := range writes {
		if w.val == nil {
			if _, err := tx.Exec(ctx, `DELETE FROM property_values WHERE entity_type=$1 AND entity_id=$2 AND def_id=$3`, string(ref.Type), ref.ID, w.def.ID); err != nil {
				return fmt.Errorf("crm: clear field: %w", err)
			}
			continue
		}
		raw, _ := json.Marshal(w.val)
		if _, err := tx.Exec(ctx, `INSERT INTO property_values (entity_type, entity_id, def_id, value, updated_by) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (entity_type, entity_id, def_id) DO UPDATE SET value=EXCLUDED.value, updated_by=EXCLUDED.updated_by, updated_at=now()`,
			string(ref.Type), ref.ID, w.def.ID, raw, actor.Principal()); err != nil {
			return fmt.Errorf("crm: set field: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE `+map[core.EntityType]string{core.EntityCompany: "companies", core.EntityContact: "contacts", core.EntityDeal: "deals"}[ref.Type]+` SET updated_at=now() WHERE id=$1 AND org_id=$2`, ref.ID, orgID); err != nil {
		return fmt.Errorf("crm: touch record: %w", err)
	}
	return tx.Commit(ctx)
}

// Props is the custom fields side of the CRM, beside Sales and Desk. *Service is the only one.
type Props interface {
	PropertyDefs(ctx context.Context, orgID string, kind core.EntityType) ([]PropertyDef, error)
	SaveProperty(ctx context.Context, orgID string, actor core.Subjects, in PropertyInput) (*PropertyDef, error)
	DeleteProperty(ctx context.Context, orgID string, actor core.Subjects, kind core.EntityType, id string) error
	PropertiesFor(ctx context.Context, orgID string, actor core.Subjects, ref core.Ref) ([]PropertyValue, error)
	SetProperties(ctx context.Context, orgID string, actor core.Subjects, ref core.Ref, values map[string]any) error
	ValidateProperties(ctx context.Context, orgID string, kind core.EntityType, values map[string]any) error
}

var _ Props = (*Service)(nil)

// ValidateProperties checks values against the fields of a kind without writing anything, so a request can refuse a bad one
// before it changes a record.
func (s *Service) ValidateProperties(ctx context.Context, orgID string, kind core.EntityType, values map[string]any) error {
	if len(values) == 0 {
		return nil
	}
	defs, err := s.PropertyDefs(ctx, orgID, kind)
	if err != nil {
		return err
	}
	byKey := map[string]PropertyDef{}
	for _, d := range defs {
		byKey[d.Key] = d
	}
	for k, v := range values {
		d, ok := byKey[k]
		if !ok {
			return fmt.Errorf("%w: there is no custom field %q", ErrInvalid, k)
		}
		if _, err := encodeProperty(d, v); err != nil {
			return err
		}
	}
	return nil
}
