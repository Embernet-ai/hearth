package main

// Bringing a Zoho CRM Contacts export into Hearth's CRM.
//
//	hearthctl zoho-contacts-import -file Contacts.xlsx -url https://hearth.example.com -owner PERSON_ID -dry-run
//	hearthctl zoho-contacts-import -file Contacts.xlsx -url https://hearth.example.com -owner PERSON_ID -manifest ids.jsonl
//
// It goes through Hearth's HTTP API with an API token that has the write scope
// (HEARTH_API_TOKEN, which keeps it out of the process list), so it needs no
// database credentials and works against any deployment. Safe to run twice: see
// internal/importer/zohocrm for what is matched and what is never changed.
//
// The manifest it writes holds ids only. To take an import back out, give
// crm-delete the company ids in it (a company goes with its contacts) and the
// ids of the contacts that have no company.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Embernet-ai/hearth/internal/importer/zohocrm"
)

func runZohoContactsImport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("zoho-contacts-import", flag.ExitOnError)
	file := fs.String("file", "", "the Zoho Contacts export, an .xlsx")
	site := envFlag(fs, "url", "HEARTH_URL", "the Hearth site, https://hearth.example.com, or HEARTH_URL")
	owner := fs.String("owner", "", "the id of the person every company and contact is assigned to (their /people/<id>)")
	token := fs.String("token", "", "an API token with the write scope; prefer HEARTH_API_TOKEN")
	dry := fs.Bool("dry-run", false, "read the file and Hearth, print what would happen, write nothing")
	manifest := fs.String("manifest", "", "write one line of ids per company and contact made or found to this file")
	rate := fs.Float64("rate", 8, "requests per second")
	limit := fs.Int("limit", 0, "import only the first N rows, to prove a run on a few before the rest; zero means all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *token == "" {
		*token = os.Getenv("HEARTH_API_TOKEN")
	}
	switch {
	case strings.TrimSpace(*file) == "":
		return errors.New("zoho-contacts-import needs -file")
	case strings.TrimSpace(*site) == "":
		return errors.New("zoho-contacts-import needs -url or HEARTH_URL")
	case strings.TrimSpace(*owner) == "":
		return errors.New("zoho-contacts-import needs -owner, the person's id")
	case strings.TrimSpace(*token) == "":
		return errors.New("zoho-contacts-import needs HEARTH_API_TOKEN or -token")
	}
	rows, err := zohocrm.ReadXLSX(*file)
	if err != nil {
		return err
	}
	if *limit > 0 && *limit < len(rows) {
		rows = rows[:*limit]
	}
	cfg := zohocrm.Config{BaseURL: *site, Token: *token, OwnerID: *owner, DryRun: *dry, PerSecond: *rate, Progress: os.Stderr}
	if *manifest != "" {
		m, err := os.OpenFile(*manifest, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer m.Close()
		cfg.Manifest = m
	}
	rep, err := zohocrm.Run(ctx, cfg, rows)
	if rep != nil {
		fmt.Printf("rows read              %d\naccounts               %d\n", rep.Rows, rep.Accounts)
		fmt.Printf("companies created      %d\ncompanies existing     %d\n", rep.CompaniesCreated, rep.CompaniesExisting)
		fmt.Printf("contacts created       %d\ncontacts existing      %d\ncontacts failed        %d\n", rep.ContactsCreated, rep.ContactsExisting, rep.ContactsFailed)
		fmt.Printf("contact name mismatches %d (Contact Name differs from First and Last; not kept)\n", rep.NameMismatches)
		if len(rep.UnusedColumns) > 0 {
			fmt.Printf("columns with data that are NOT moved: %s\n", strings.Join(rep.UnusedColumns, ", "))
		}
		for _, e := range rep.Errors {
			fmt.Println("error:", e)
		}
		if *dry {
			fmt.Println("dry run: nothing was written")
		}
	}
	return err
}
