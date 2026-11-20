package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Embernet-ai/hearth/internal/auth"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/crm/csvimport"
)

// CRMImportResult is what a CSV import, or a check of one, found.
type CRMImportResult struct {
	Kind  string
	Check bool
	// Total is the records in the file. Created and Skipped (already there) and
	// Failed add up to it.
	Total, Created, Skipped, Failed int
	// CompaniesCreated counts firms a contact or deal named that did not exist.
	CompaniesCreated int
	Problems         []string
	MoreProblems     int
	Used             []csvimport.Mapping
	Ignored          []string
	Truncated        bool
	MaxRows          int
}

const maxProblemsShown = 50

func (s *Server) getCRMImport(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "crmimport", page{Title: "Import contacts, companies and deals", SalesOn: s.sales != nil})
}

// postCRMImport reads one CSV and creates its companies, contacts or deals. With
// mode=check it reads and resolves everything and creates nothing, so a person
// sees the counts and the problems first. Running a file twice is safe: what is
// already there is skipped, never changed or doubled.
func (s *Server) postCRMImport(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	again := func(msg string) {
		s.render(w, r, "crmimport", page{Title: "Import contacts, companies and deals", SalesOn: s.sales != nil, Message: msg})
	}
	if err := r.ParseMultipartForm(maxUploadForm); err != nil {
		s.fail(w, r, http.StatusBadRequest, "could not read the upload")
		return
	}
	kind := csvimport.Kind(r.FormValue("kind"))
	if !kind.Valid() || (kind == csvimport.Deals && s.sales == nil) {
		again("Choose what the file holds.")
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		again("Choose a CSV file first.")
		return
	}
	defer f.Close()
	sheet, err := csvimport.Parse(kind, f)
	if err != nil {
		again("That file could not be used: " + err.Error() + ".")
		return
	}
	check := r.FormValue("mode") != "import"
	res := s.runCRMImport(r, id, sheet, check)
	s.render(w, r, "crmimport", page{Title: "Import contacts, companies and deals", SalesOn: s.sales != nil, CRMImport: res})
}

// importIndex finds firms already in Hearth by web domain first, then by name.
type importIndex struct {
	byDomain map[string]string
	byName   map[string]string
}

func domainKey(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if u, err := url.Parse(v); err == nil && u.Host != "" {
		v = u.Host
	}
	v = strings.TrimPrefix(v, "www.")
	if i := strings.IndexAny(v, "/?#"); i >= 0 {
		v = v[:i]
	}
	return v
}

func (x *importIndex) add(c *crm.Company) {
	if d := domainKey(c.Domain); d != "" {
		x.byDomain[d] = c.ID
	}
	x.byName[strings.ToLower(strings.TrimSpace(c.Name))] = c.ID
}

func (x *importIndex) find(name, domain string) string {
	if d := domainKey(domain); d != "" {
		if id, ok := x.byDomain[d]; ok {
			return id
		}
	}
	return x.byName[strings.ToLower(strings.TrimSpace(name))]
}

func (s *Server) runCRMImport(r *http.Request, id *auth.Identity, sheet *csvimport.Sheet, check bool) *CRMImportResult {
	ctx := r.Context()
	res := &CRMImportResult{Kind: string(sheet.Kind), Check: check, Total: len(sheet.Rows), Used: sheet.Used,
		Ignored: sheet.Ignored, Truncated: sheet.Truncated, MaxRows: csvimport.MaxRows}
	problem := func(line int, msg string) {
		res.Failed++
		if len(res.Problems) < maxProblemsShown {
			res.Problems = append(res.Problems, fmt.Sprintf("Line %d: %s", line, msg))
		} else {
			res.MoreProblems++
		}
	}

	idx := &importIndex{byDomain: map[string]string{}, byName: map[string]string{}}
	if existing, err := s.crm.Companies(ctx, id.OrgID, id.Subjects, "", ""); err == nil {
		for _, c := range existing {
			idx.add(c)
		}
	} else {
		s.log.ErrorContext(ctx, "crm import: load companies", "error", err)
	}

	// company returns the id for the firm a row names, making it when it is new.
	// In a check the id is a stand in, so later rows of the same file still see the firm.
	pending := 0
	company := func(name string) (string, error) {
		name = strings.TrimSpace(name)
		if name == "" {
			return "", nil
		}
		if got := idx.find(name, ""); got != "" {
			return got, nil
		}
		res.CompaniesCreated++
		if check {
			pending++
			stand := fmt.Sprintf("pending-%d", pending)
			idx.byName[strings.ToLower(name)] = stand
			return stand, nil
		}
		made, err := s.crm.CreateCompany(ctx, id.OrgID, id.Subjects, crm.Company{Name: name, Kind: crm.KindOther})
		if err != nil {
			res.CompaniesCreated--
			return "", err
		}
		s.shareNew(ctx, id, made.Ref())
		idx.add(made)
		return made.ID, nil
	}

	var owners map[string]string
	if sheet.Kind == csvimport.Deals {
		owners = map[string]string{}
		for _, p := range s.owners(r) {
			owners[strings.ToLower(strings.TrimSpace(p.Email))] = p.PrincipalID
		}
	}
	seenDeals := map[string]bool{}
	if sheet.Kind == csvimport.Deals {
		for off := 0; ; off += 5000 {
			list, total, err := s.sales.Deals(ctx, id.OrgID, id.Subjects, crm.ListQuery{Limit: 5000, Offset: off})
			if err != nil {
				break
			}
			for _, d := range list {
				seenDeals[dealKey(d.Name, deref(d.CompanyID))] = true
			}
			if off+5000 >= total || len(list) == 0 {
				break
			}
		}
	}
	seenInFile := map[string]bool{}

	for _, row := range sheet.Rows {
		if row.Err != "" {
			problem(row.Line, row.Err)
			continue
		}
		f := row.Fields
		switch sheet.Kind {
		case csvimport.Companies:
			if idx.find(f["name"], f["domain"]) != "" {
				res.Skipped++
				continue
			}
			c := crm.Company{Name: f["name"], Domain: f["domain"], Kind: csvimport.CompanyKind(f["kind"]), Notes: f["notes"]}
			if check {
				res.Created++
				stand := fmt.Sprintf("pending-%d", res.Created)
				idx.byName[strings.ToLower(c.Name)] = stand
				if d := domainKey(c.Domain); d != "" {
					idx.byDomain[d] = stand
				}
				continue
			}
			made, err := s.crm.CreateCompany(ctx, id.OrgID, id.Subjects, c)
			if err != nil {
				problem(row.Line, importErr(err))
				continue
			}
			s.shareNew(ctx, id, made.Ref())
			idx.add(made)
			res.Created++

		case csvimport.Contacts:
			email := strings.ToLower(f["email"])
			if email != "" {
				if seenInFile[email] {
					res.Skipped++
					continue
				}
				if _, err := s.crm.ByEmail(ctx, id.OrgID, email); err == nil {
					res.Skipped++
					continue
				}
				seenInFile[email] = true
			}
			c := crm.Contact{Name: f["name"], Email: email, Phone: f["phone"], Title: f["title"], Notes: f["notes"]}
			if c.Name == "" {
				c.Name = email
			}
			if cid, err := company(f["company"]); err != nil {
				problem(row.Line, "the company: "+importErr(err))
				continue
			} else if cid != "" {
				if !check {
					c.CompanyID = &cid
				}
			}
			if check {
				res.Created++
				continue
			}
			made, err := s.crm.CreateContact(ctx, id.OrgID, id.Subjects, c)
			if err != nil {
				problem(row.Line, importErr(err))
				continue
			}
			s.shareNew(ctx, id, made.Ref())
			res.Created++

		case csvimport.Deals:
			stage, err := csvimport.Stage(f["stage"])
			if err != nil {
				problem(row.Line, err.Error())
				continue
			}
			cents, err := csvimport.Cents(f["amount"])
			if err != nil {
				problem(row.Line, err.Error())
				continue
			}
			closeDate, err := csvimport.Date(f["close_date"])
			if err != nil {
				problem(row.Line, err.Error())
				continue
			}
			d := crm.Deal{Name: f["name"], Stage: stage, AmountCents: cents, Currency: strings.ToUpper(f["currency"]),
				CloseDate: closeDate, Notes: f["notes"]}
			if email := strings.ToLower(f["owner_email"]); email != "" {
				pid, ok := owners[email]
				if !ok {
					problem(row.Line, fmt.Sprintf("no active person has the email %s", email))
					continue
				}
				d.OwnerID = &pid
			}
			cid, err := company(f["company"])
			if err != nil {
				problem(row.Line, "the company: "+importErr(err))
				continue
			}
			if cid != "" && !check {
				d.CompanyID = &cid
			}
			if email := strings.ToLower(f["contact_email"]); email != "" && strings.Contains(email, "@") {
				if c, err := s.crm.ByEmail(ctx, id.OrgID, email); err == nil {
					d.ContactID = &c.ID
				}
			}
			key := dealKey(d.Name, cid)
			if seenDeals[key] || seenInFile[key] {
				res.Skipped++
				continue
			}
			seenInFile[key] = true
			if check {
				res.Created++
				continue
			}
			if _, err := s.sales.CreateDeal(ctx, id.OrgID, id.Subjects, d); err != nil {
				problem(row.Line, importErr(err))
				continue
			}
			res.Created++
		}
	}
	return res
}

func dealKey(name, companyID string) string {
	return strings.ToLower(strings.TrimSpace(name)) + "|" + companyID
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// shareNew gives the sales team the record, as the single record forms do.
func (s *Server) shareNew(ctx context.Context, id *auth.Identity, ref core.Ref) {
	if s.sales == nil {
		return
	}
	if err := s.sales.Share(ctx, id.OrgID, id.Subjects, ref); err != nil {
		s.log.WarnContext(ctx, "share imported record", "error", err)
	}
}

// importErr is a sentence a person can act on.
func importErr(err error) string {
	switch {
	case errors.Is(err, crm.ErrInvalid):
		return strings.TrimPrefix(err.Error(), "crm: ")
	default:
		return "not allowed or could not be saved (" + err.Error() + ")"
	}
}
