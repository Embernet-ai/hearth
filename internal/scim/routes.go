package scim

// The protocol surface: Users and Groups, over plain net/http and encoding/json.
//
// Written out rather than pulled in, for the same reason the Markdown renderer
// and the QR encoder were. What a provisioning job sends is a small, fixed set
// of shapes, and a library covering the whole specification is a dependency
// carrying five times the surface anybody here will ever exercise.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Embernet-ai/hearth/internal/teams"
)

// Routes is the whole endpoint, mounted under /scim/v2.
func (s *Service) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /Users", s.listUsers)
	mux.HandleFunc("POST /Users", s.createUser)
	mux.HandleFunc("GET /Users/{id}", s.getUser)
	mux.HandleFunc("PUT /Users/{id}", s.replaceUser)
	mux.HandleFunc("PATCH /Users/{id}", s.patchUser)
	mux.HandleFunc("DELETE /Users/{id}", s.deleteUser)

	mux.HandleFunc("GET /Groups", s.listGroups)
	mux.HandleFunc("POST /Groups", s.createGroup)
	mux.HandleFunc("GET /Groups/{id}", s.getGroup)
	mux.HandleFunc("PUT /Groups/{id}", s.replaceGroup)
	mux.HandleFunc("PATCH /Groups/{id}", s.patchGroup)
	mux.HandleFunc("DELETE /Groups/{id}", s.deleteGroup)

	// Everything else answers in the protocol's own error shape rather than
	// with a Go default, because a provisioning job that gets an HTML 404 logs
	// "unexpected response" and tells nobody which endpoint it wanted.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.fail(w, http.StatusNotFound, "no such endpoint: "+r.URL.Path)
	})

	return s.authenticated(mux)
}

// orgKey carries the provisioned org from the middleware to the handlers,
// through the request rather than through a field on the service.
type orgKey struct{}

func (s *Service) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		orgID, err := s.authenticate(r.Context(), r)
		if errors.Is(err, ErrNoToken) {
			// The header matters: without it a provisioning job retries with the
			// same credential forever instead of reporting a configuration
			// problem to whoever set it up.
			w.Header().Set("WWW-Authenticate", `Bearer realm="hearth"`)
			s.fail(w, http.StatusUnauthorized, "that token is not valid here")
			return
		}
		if err != nil {
			s.log.ErrorContext(r.Context(), "scim authenticate", "error", err)
			s.fail(w, http.StatusInternalServerError, "could not check that token")
			return
		}
		ctx := context.WithValue(r.Context(), orgKey{}, orgID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func orgOf(r *http.Request) string {
	v, _ := r.Context().Value(orgKey{}).(string)
	return v
}

// ---------------------------------------------------------------------------
// The wire shapes
// ---------------------------------------------------------------------------

type userResource struct {
	Schemas    []string `json:"schemas"`
	ID         string   `json:"id"`
	ExternalID string   `json:"externalId,omitempty"`
	UserName   string   `json:"userName"`
	Active     bool     `json:"active"`
	Name       *struct {
		Formatted  string `json:"formatted,omitempty"`
		GivenName  string `json:"givenName,omitempty"`
		FamilyName string `json:"familyName,omitempty"`
	} `json:"name,omitempty"`
	DisplayName string  `json:"displayName,omitempty"`
	Emails      []email `json:"emails,omitempty"`
	Meta        meta    `json:"meta"`
}

type email struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary,omitempty"`
	Type    string `json:"type,omitempty"`
}

type groupResource struct {
	Schemas     []string `json:"schemas"`
	ID          string   `json:"id"`
	ExternalID  string   `json:"externalId,omitempty"`
	DisplayName string   `json:"displayName"`
	Members     []member `json:"members"`
	Meta        meta     `json:"meta"`
}

type member struct {
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
	Ref     string `json:"$ref,omitempty"`
}

type meta struct {
	ResourceType string `json:"resourceType"`
	Created      string `json:"created,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
	Location     string `json:"location,omitempty"`
}

type listResponse struct {
	Schemas      []string `json:"schemas"`
	TotalResults int      `json:"totalResults"`
	ItemsPerPage int      `json:"itemsPerPage"`
	StartIndex   int      `json:"startIndex"`
	Resources    []any    `json:"Resources"`
}

// patchRequest is what a PATCH carries.
//
// Value is deliberately json.RawMessage. A membership operation sends an array
// of objects, an active operation sends a bare boolean or the string "False",
// and a displayName operation sends a string. One field with three shapes is
// what the protocol actually puts on the wire, and decoding it into `any` and
// type switching produces worse code than decoding it twice on purpose.
type patchRequest struct {
	Schemas    []string `json:"schemas"`
	Operations []struct {
		Op    string          `json:"op"`
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value"`
	} `json:"Operations"`
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

func (s *Service) createUser(w http.ResponseWriter, r *http.Request) {
	orgID := orgOf(r)
	var in struct {
		ExternalID  string  `json:"externalId"`
		UserName    string  `json:"userName"`
		DisplayName string  `json:"displayName"`
		Active      *bool   `json:"active"`
		Emails      []email `json:"emails"`
		Name        struct {
			Formatted  string `json:"formatted"`
			GivenName  string `json:"givenName"`
			FamilyName string `json:"familyName"`
		} `json:"name"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	address := primaryEmail(in.Emails, in.UserName)
	if address == "" {
		s.fail(w, http.StatusBadRequest, "a user needs a userName or an email address")
		return
	}
	name := firstNonEmpty(in.DisplayName, in.Name.Formatted,
		strings.TrimSpace(in.Name.GivenName+" "+in.Name.FamilyName), localPart(address))

	id, err := s.users.ProvisionUser(r.Context(), orgID, in.ExternalID, name, address)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	if in.Active != nil && !*in.Active {
		if err := s.users.SetProvisionedStatus(r.Context(), orgID, id, "suspended"); err != nil {
			s.problem(w, r, err)
			return
		}
	}
	res, err := s.readUser(r, orgID, id)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	// 201 with a Location, which is what a provisioning job stores so it never
	// has to search for this person again.
	w.Header().Set("Location", res.Meta.Location)
	s.write(w, http.StatusCreated, res)
}

func (s *Service) getUser(w http.ResponseWriter, r *http.Request) {
	res, err := s.readUser(r, orgOf(r), r.PathValue("id"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	s.write(w, http.StatusOK, res)
}

// listUsers answers the one search a provisioning job makes: find this
// userName, so it can decide between creating and updating.
func (s *Service) listUsers(w http.ResponseWriter, r *http.Request) {
	orgID := orgOf(r)
	field, value, err := parseFilter(r.URL.Query().Get("filter"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}

	where, args := `p.org_id = $1 AND p.kind = 'user'`, []any{orgID}
	switch field {
	case "":
	case "username":
		args = append(args, value)
		where += fmt.Sprintf(" AND lower(c.email) = lower($%d)", len(args))
	case "externalid":
		args = append(args, value)
		where += fmt.Sprintf(" AND p.external_id = $%d", len(args))
	default:
		s.fail(w, http.StatusBadRequest, "this endpoint filters on userName or externalId, not "+field)
		return
	}

	rows, err := s.db.Query(r.Context(), `
		SELECT p.id::text, COALESCE(p.external_id,''), COALESCE(c.email::text,''),
		       p.display_name, p.status, p.created_at, p.updated_at
		  FROM principals p
		  LEFT JOIN local_credentials c ON c.principal_id = p.id
		 WHERE `+where+`
		 ORDER BY p.created_at
		 LIMIT 200`, args...)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	defer rows.Close()

	out := []any{}
	for rows.Next() {
		u, err := scanUser(rows, r)
		if err != nil {
			s.problem(w, r, err)
			return
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		s.problem(w, r, err)
		return
	}
	s.write(w, http.StatusOK, listResponse{
		Schemas: []string{schemaList}, TotalResults: len(out),
		ItemsPerPage: len(out), StartIndex: 1, Resources: out,
	})
}

// replaceUser is a PUT, which Entra sends when it decides the whole record
// should look like this.
func (s *Service) replaceUser(w http.ResponseWriter, r *http.Request) {
	orgID, id := orgOf(r), r.PathValue("id")
	var in struct {
		DisplayName string  `json:"displayName"`
		Active      *bool   `json:"active"`
		Emails      []email `json:"emails"`
		UserName    string  `json:"userName"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if err := s.ensureUser(r, orgID, id); err != nil {
		s.problem(w, r, err)
		return
	}
	if name := strings.TrimSpace(in.DisplayName); name != "" {
		if _, err := s.db.Exec(r.Context(),
			`UPDATE principals SET display_name=$3, updated_at=now()
			  WHERE id=$1 AND org_id=$2`, id, orgID, name); err != nil {
			s.problem(w, r, err)
			return
		}
	}
	if in.Active != nil {
		if err := s.setActive(r, orgID, id, *in.Active); err != nil {
			s.problem(w, r, err)
			return
		}
	}
	res, err := s.readUser(r, orgID, id)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	s.write(w, http.StatusOK, res)
}

// patchUser handles the operation that matters: active going false.
func (s *Service) patchUser(w http.ResponseWriter, r *http.Request) {
	orgID, id := orgOf(r), r.PathValue("id")
	var in patchRequest
	if !s.decode(w, r, &in) {
		return
	}
	if err := s.ensureUser(r, orgID, id); err != nil {
		s.problem(w, r, err)
		return
	}

	for _, op := range in.Operations {
		path := strings.ToLower(strings.TrimSpace(op.Path))
		switch {
		case path == "active":
			active, ok := parseBool(op.Value)
			if !ok {
				s.fail(w, http.StatusBadRequest, "active has to be true or false")
				return
			}
			if err := s.setActive(r, orgID, id, active); err != nil {
				s.problem(w, r, err)
				return
			}
		case path == "displayname":
			var name string
			if err := json.Unmarshal(op.Value, &name); err == nil && strings.TrimSpace(name) != "" {
				if _, err := s.db.Exec(r.Context(),
					`UPDATE principals SET display_name=$3, updated_at=now()
					  WHERE id=$1 AND org_id=$2`, id, orgID, strings.TrimSpace(name)); err != nil {
					s.problem(w, r, err)
					return
				}
			}
		case path == "":
			// A pathless replace carries an object of attributes. Entra sends
			// this shape for deactivation as often as it sends the other one.
			var body struct {
				Active      *bool  `json:"active"`
				DisplayName string `json:"displayName"`
			}
			if err := json.Unmarshal(op.Value, &body); err == nil {
				if body.Active != nil {
					if err := s.setActive(r, orgID, id, *body.Active); err != nil {
						s.problem(w, r, err)
						return
					}
				}
				if strings.TrimSpace(body.DisplayName) != "" {
					if _, err := s.db.Exec(r.Context(),
						`UPDATE principals SET display_name=$3, updated_at=now()
						  WHERE id=$1 AND org_id=$2`, id, orgID,
						strings.TrimSpace(body.DisplayName)); err != nil {
						s.problem(w, r, err)
						return
					}
				}
			}
		default:
			// Ignored rather than refused. A provisioning job maps a dozen
			// attributes this product has no column for, and failing the whole
			// operation over a job title stops the deactivation in the same
			// request from happening.
			s.log.InfoContext(r.Context(), "scim: ignored a user attribute",
				"path", op.Path, "op", op.Op)
		}
	}
	res, err := s.readUser(r, orgID, id)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	s.write(w, http.StatusOK, res)
}

// deleteUser suspends rather than deletes.
//
// A principal is the foreign key on every "who did this" in this database, so
// deleting one takes the history with it. The protocol says the resource
// should stop appearing, and a suspended account cannot sign in, holds no
// session, and appears in no picker, which is the part that matters.
func (s *Service) deleteUser(w http.ResponseWriter, r *http.Request) {
	orgID, id := orgOf(r), r.PathValue("id")
	if err := s.ensureUser(r, orgID, id); err != nil {
		s.problem(w, r, err)
		return
	}
	if err := s.setActive(r, orgID, id, false); err != nil {
		s.problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) setActive(r *http.Request, orgID, id string, active bool) error {
	status := "suspended"
	if active {
		status = "active"
	}
	return s.users.SetProvisionedStatus(r.Context(), orgID, id, status)
}

func (s *Service) ensureUser(r *http.Request, orgID, id string) error {
	if !looksLikeUUID(id) {
		return ErrNotFound
	}
	var exists bool
	if err := s.db.QueryRow(r.Context(),
		`SELECT EXISTS (SELECT 1 FROM principals WHERE id=$1 AND org_id=$2 AND kind='user')`,
		id, orgID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

func (s *Service) readUser(r *http.Request, orgID, id string) (*userResource, error) {
	if !looksLikeUUID(id) {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT p.id::text, COALESCE(p.external_id,''), COALESCE(c.email::text,''),
		       p.display_name, p.status, p.created_at, p.updated_at
		  FROM principals p
		  LEFT JOIN local_credentials c ON c.principal_id = p.id
		 WHERE p.id=$1 AND p.org_id=$2 AND p.kind='user'`, id, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrNotFound
	}
	return scanUser(rows, r)
}

type scanner interface{ Scan(dest ...any) error }

func scanUser(rows scanner, r *http.Request) (*userResource, error) {
	var id, external, address, name, status string
	var created, updated timeValue
	if err := rows.Scan(&id, &external, &address, &name, &status, &created, &updated); err != nil {
		return nil, err
	}
	u := &userResource{
		Schemas: []string{schemaUser}, ID: id, ExternalID: external,
		UserName: address, DisplayName: name, Active: status == "active",
		Meta: meta{ResourceType: "User", Created: created.iso(),
			LastModified: updated.iso(), Location: location(r, "Users", id)},
	}
	if address != "" {
		u.Emails = []email{{Value: address, Primary: true, Type: "work"}}
	}
	return u, nil
}

// ---------------------------------------------------------------------------
// Groups
// ---------------------------------------------------------------------------

func (s *Service) createGroup(w http.ResponseWriter, r *http.Request) {
	orgID := orgOf(r)
	var in struct {
		ExternalID  string   `json:"externalId"`
		DisplayName string   `json:"displayName"`
		Members     []member `json:"members"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	external := firstNonEmpty(in.ExternalID, in.DisplayName)
	team, err := s.teams.CreateManaged(r.Context(), orgID, "", in.DisplayName, external)
	if errors.Is(err, teams.ErrInvalid) && strings.Contains(err.Error(), "already a team") {
		// A group whose name collides with a team somebody made by hand. Said
		// plainly, because the fix is a person renaming one of them and a
		// provisioning job will otherwise retry this forever.
		s.fail(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		s.problem(w, r, err)
		return
	}
	if len(in.Members) > 0 {
		if _, _, err := s.teams.SetMembers(r.Context(), orgID, "", team.ID, valuesOf(in.Members)); err != nil {
			s.problem(w, r, err)
			return
		}
	}
	res, err := s.readGroup(r, orgID, team.ID)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	w.Header().Set("Location", res.Meta.Location)
	s.write(w, http.StatusCreated, res)
}

func (s *Service) getGroup(w http.ResponseWriter, r *http.Request) {
	res, err := s.readGroup(r, orgOf(r), r.PathValue("id"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	s.write(w, http.StatusOK, res)
}

func (s *Service) listGroups(w http.ResponseWriter, r *http.Request) {
	orgID := orgOf(r)
	field, value, err := parseFilter(r.URL.Query().Get("filter"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	all, err := s.teams.List(r.Context(), orgID)
	if err != nil {
		s.problem(w, r, err)
		return
	}

	out := []any{}
	for _, t := range all {
		switch field {
		case "":
		case "displayname":
			if !strings.EqualFold(t.Name, value) {
				continue
			}
		case "externalid":
			if t.ExternalID == nil || *t.ExternalID != value {
				continue
			}
		default:
			s.fail(w, http.StatusBadRequest,
				"this endpoint filters on displayName or externalId, not "+field)
			return
		}
		res, err := s.readGroup(r, orgID, t.ID)
		if err != nil {
			s.problem(w, r, err)
			return
		}
		out = append(out, res)
	}
	s.write(w, http.StatusOK, listResponse{
		Schemas: []string{schemaList}, TotalResults: len(out),
		ItemsPerPage: len(out), StartIndex: 1, Resources: out,
	})
}

func (s *Service) replaceGroup(w http.ResponseWriter, r *http.Request) {
	orgID, id := orgOf(r), r.PathValue("id")
	var in struct {
		DisplayName string   `json:"displayName"`
		Members     []member `json:"members"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.DisplayName) != "" {
		if err := s.teams.RenameManaged(r.Context(), orgID, "", id, in.DisplayName); err != nil {
			s.problem(w, r, err)
			return
		}
	}
	if _, _, err := s.teams.SetMembers(r.Context(), orgID, "", id, valuesOf(in.Members)); err != nil {
		s.problem(w, r, err)
		return
	}
	res, err := s.readGroup(r, orgID, id)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	s.write(w, http.StatusOK, res)
}

// patchGroup is the endpoint the whole criterion rests on.
//
// Removing somebody from a group in Entra arrives here as a remove operation,
// and by the time this returns the team membership is gone. Their next request
// resolves a subject set without that team in it, because subjects are worked
// out per request and never kept in the session.
func (s *Service) patchGroup(w http.ResponseWriter, r *http.Request) {
	orgID, id := orgOf(r), r.PathValue("id")
	var in patchRequest
	if !s.decode(w, r, &in) {
		return
	}
	if _, err := s.teams.Get(r.Context(), orgID, id); err != nil {
		s.problem(w, r, err)
		return
	}

	for _, op := range in.Operations {
		verb := strings.ToLower(strings.TrimSpace(op.Op))
		path := strings.ToLower(strings.TrimSpace(op.Path))

		// Entra removes one member with a filtered path and no value at all:
		//   {"op":"remove","path":"members[value eq \"<id>\"]"}
		// and also with a plain path and a value array. Both shapes are the
		// same instruction and both have to work, because which one arrives
		// depends on the tenant's provisioning mode rather than on anything
		// here.
		if verb == "remove" && strings.HasPrefix(path, "members[") {
			who := valueInFilter(op.Path)
			if who == "" {
				s.fail(w, http.StatusBadRequest, "that membership filter names nobody")
				return
			}
			if err := s.teams.Remove(r.Context(), orgID, "", id, who); err != nil {
				s.problem(w, r, err)
				return
			}
			continue
		}

		switch {
		case path == "members" || path == "":
			var list []member
			if err := json.Unmarshal(op.Value, &list); err != nil {
				// A pathless operation whose value is the whole resource.
				var body struct {
					DisplayName string   `json:"displayName"`
					Members     []member `json:"members"`
				}
				if err := json.Unmarshal(op.Value, &body); err != nil {
					s.fail(w, http.StatusBadRequest, "that operation carries no members")
					return
				}
				if strings.TrimSpace(body.DisplayName) != "" {
					if err := s.teams.RenameManaged(r.Context(), orgID, "", id, body.DisplayName); err != nil {
						s.problem(w, r, err)
						return
					}
				}
				list = body.Members
			}
			if err := s.applyMembers(r, orgID, id, verb, valuesOf(list)); err != nil {
				s.problem(w, r, err)
				return
			}
		case path == "displayname":
			var name string
			if err := json.Unmarshal(op.Value, &name); err == nil && strings.TrimSpace(name) != "" {
				if err := s.teams.RenameManaged(r.Context(), orgID, "", id, name); err != nil {
					s.problem(w, r, err)
					return
				}
			}
		default:
			s.log.InfoContext(r.Context(), "scim: ignored a group attribute",
				"path", op.Path, "op", op.Op)
		}
	}
	res, err := s.readGroup(r, orgID, id)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	s.write(w, http.StatusOK, res)
}

func (s *Service) applyMembers(r *http.Request, orgID, teamID, verb string, ids []string) error {
	switch verb {
	case "add":
		for _, id := range ids {
			if err := s.teams.Add(r.Context(), orgID, "", teamID, id); err != nil {
				// A member the directory knows and this installation does not
				// is normal during a first sync: the users job and the groups
				// job run separately and either can be first. Skipping is
				// right; failing would stop the whole group.
				if errors.Is(err, teams.ErrInvalid) {
					s.log.InfoContext(r.Context(), "scim: group names somebody not here yet",
						"principal", id, "team", teamID)
					continue
				}
				return err
			}
		}
		return nil
	case "remove":
		for _, id := range ids {
			if err := s.teams.Remove(r.Context(), orgID, "", teamID, id); err != nil {
				return err
			}
		}
		return nil
	default: // replace
		_, _, err := s.teams.SetMembers(r.Context(), orgID, "", teamID, ids)
		return err
	}
}

func (s *Service) deleteGroup(w http.ResponseWriter, r *http.Request) {
	orgID, id := orgOf(r), r.PathValue("id")
	if err := s.teams.Delete(r.Context(), orgID, "", id); err != nil {
		s.problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) readGroup(r *http.Request, orgID, id string) (*groupResource, error) {
	if !looksLikeUUID(id) {
		return nil, ErrNotFound
	}
	t, err := s.teams.Get(r.Context(), orgID, id)
	if err != nil {
		return nil, err
	}
	people, err := s.teams.Members(r.Context(), orgID, id)
	if err != nil {
		return nil, err
	}
	res := &groupResource{
		Schemas: []string{schemaGroup}, ID: t.ID, DisplayName: t.Name,
		Members: []member{},
		Meta: meta{ResourceType: "Group", Created: iso(t.CreatedAt),
			Location: location(r, "Groups", t.ID)},
	}
	if t.ExternalID != nil {
		res.ExternalID = *t.ExternalID
	}
	for _, p := range people {
		res.Members = append(res.Members, member{
			Value: p.PrincipalID, Display: p.Name,
			Ref: location(r, "Users", p.PrincipalID),
		})
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// Plumbing
// ---------------------------------------------------------------------------

func (s *Service) decode(w http.ResponseWriter, r *http.Request, into any) bool {
	// Bounded. A provisioning job sends small documents and an unbounded read
	// is an unbounded read whoever is at the other end.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(into); err != nil {
		s.fail(w, http.StatusBadRequest, "that body is not SCIM JSON: "+err.Error())
		return false
	}
	return true
}

func (s *Service) write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", contentTypeSCIM+"; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Error("scim: write response", "error", err)
	}
}

// fail answers in the protocol's own error shape.
func (s *Service) fail(w http.ResponseWriter, code int, detail string) {
	w.Header().Set("Content-Type", contentTypeSCIM+"; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"schemas": []string{schemaError},
		"status":  strconv.Itoa(code),
		"detail":  detail,
	})
}

// problem turns an internal error into the right status without leaking what
// went wrong to a caller that is not a person.
func (s *Service) problem(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, teams.ErrNotFound):
		s.fail(w, http.StatusNotFound, "no such resource")
	case errors.Is(err, teams.ErrInvalid), errors.Is(err, teams.ErrManaged):
		s.fail(w, http.StatusBadRequest, err.Error())
	default:
		s.log.ErrorContext(r.Context(), "scim", "path", r.URL.Path, "error", err)
		s.fail(w, http.StatusInternalServerError, "that did not work here")
	}
}

// parseFilter reads the one filter shape a provisioning job sends:
//
//	userName eq "someone@example.com"
//
// Anything more is refused with a sentence rather than silently ignored, which
// is the failure that returns every user in the tenant to a job that asked for
// one and then reconciles the difference.
func parseFilter(v string) (field, value string, err error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", "", nil
	}
	field, rest, ok := strings.Cut(v, " ")
	if !ok {
		return "", "", errors.New("that filter is not one this endpoint understands")
	}
	op, quoted, ok := strings.Cut(strings.TrimSpace(rest), " ")
	if !ok || !strings.EqualFold(op, "eq") {
		return "", "", errors.New("this endpoint only filters with eq")
	}
	quoted = strings.TrimSpace(quoted)
	if len(quoted) >= 2 && quoted[0] == '"' && quoted[len(quoted)-1] == '"' {
		var out string
		if err := json.Unmarshal([]byte(quoted), &out); err == nil {
			return strings.ToLower(field), out, nil
		}
	}
	return strings.ToLower(field), strings.Trim(quoted, `"`), nil
}

// valueInFilter pulls the id out of members[value eq "..."].
func valueInFilter(path string) string {
	open := strings.Index(path, "[")
	close := strings.LastIndex(path, "]")
	if open < 0 || close < open {
		return ""
	}
	_, value, err := parseFilter(path[open+1 : close])
	if err != nil {
		return ""
	}
	return value
}

// parseBool accepts what the protocol actually carries.
//
// A JSON boolean, and the strings "True" and "False", which some provisioning
// jobs send because their internal representation is a string and the
// specification does not stop them.
func parseBool(raw json.RawMessage) (bool, bool) {
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

func valuesOf(ms []member) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		if v := strings.TrimSpace(m.Value); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func primaryEmail(list []email, fallback string) string {
	for _, e := range list {
		if e.Primary && strings.Contains(e.Value, "@") {
			return strings.ToLower(strings.TrimSpace(e.Value))
		}
	}
	for _, e := range list {
		if strings.Contains(e.Value, "@") {
			return strings.ToLower(strings.TrimSpace(e.Value))
		}
	}
	if strings.Contains(fallback, "@") {
		return strings.ToLower(strings.TrimSpace(fallback))
	}
	return ""
}

func localPart(address string) string {
	name, _, _ := strings.Cut(address, "@")
	return name
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// location is the absolute address of a resource, which a provisioning job
// stores and comes back to.
func location(r *http.Request, kind, id string) string {
	scheme := "https"
	if r.TLS == nil && !strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "http"
	}
	return scheme + "://" + r.Host + "/scim/v2/" + kind + "/" + id
}

func looksLikeUUID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, r := range v {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}
