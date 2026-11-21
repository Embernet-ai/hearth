package main

// Keeping Hearth's CRM and EmberOps in step.
//
//	hearthctl crm-sync                       what would be sent, writing nothing (the default)
//	hearthctl crm-sync -write                send it
//	hearthctl crm-sync -write -only ID,ID    only these companies
//	hearthctl crm-sync -prospects            also every company with no accounting role, as a Lead with its people
//	hearthctl crm-sync -timeline             also notes and calls, as Comments (one way)
//	hearthctl crm-sync -deals                also deals as Opportunities (dry run unless -write)
//	hearthctl crm-sync -only ID -remove      take a proof company out of EmberOps again
//
// Reads Hearth's Postgres and talks to EmberOps with a Vord service client. It
// has its own settings and does not use HEARTH_BACKEND: Hearth keeps its data in
// Postgres, the CRM included, and only a projection goes to EmberOps.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/Embernet-ai/hearth/internal/authz"
	"github.com/Embernet-ai/hearth/internal/core"
	"github.com/Embernet-ai/hearth/internal/crm"
	"github.com/Embernet-ai/hearth/internal/crmsync"
	"github.com/Embernet-ai/hearth/internal/emberops"
	"github.com/Embernet-ai/hearth/internal/platform/pg"
)

func runCRMSync(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("crm-sync", flag.ExitOnError)
	write := fs.Bool("write", false, "send the records; without it nothing is written to either side")
	only := fs.String("only", "", "comma separated Hearth company ids; empty is every company")
	prospects := fs.Bool("prospects", false, "also send every company with no accounting role as a Lead, and its people as Contacts")
	timeline := fs.Bool("timeline", false, "also send Hearth's notes and calls as Comments on the matching EmberOps record")
	deals := fs.Bool("deals", false, "also send deals as Opportunities, with a Lead first for a prospect that has one (needs EmberOps to accept both)")
	show := fs.Bool("show", false, "read the EmberOps records of the -only companies back and print what is stored")
	remove := fs.Bool("remove", false, "delete the EmberOps records of the -only companies (for a proof company)")
	dsn := fs.String("dsn", os.Getenv("HEARTH_DATABASE_URL"), "Postgres connection string, or HEARTH_DATABASE_URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	site := strings.TrimSpace(os.Getenv("CRM_SYNC_EMBEROPS_URL"))
	issuer := strings.TrimSuffix(strings.TrimSpace(os.Getenv("HEARTH_OIDC_ISSUER")), "/")
	id, secret := strings.TrimSpace(os.Getenv("CRM_SYNC_CLIENT_ID")), os.Getenv("CRM_SYNC_CLIENT_SECRET")
	audience := os.Getenv("CRM_SYNC_AUDIENCE")
	if audience == "" {
		audience = "emberops"
	}
	for name, v := range map[string]string{"CRM_SYNC_EMBEROPS_URL": site, "HEARTH_OIDC_ISSUER": issuer,
		"CRM_SYNC_CLIENT_ID": id, "CRM_SYNC_CLIENT_SECRET": secret, "HEARTH_DATABASE_URL": *dsn} {
		if v == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	base, err := url.Parse(site)
	if err != nil || base.Host == "" {
		return errors.New("CRM_SYNC_EMBEROPS_URL is not a URL")
	}

	pools, err := pg.Open(ctx, pg.DefaultOptions(*dsn))
	if err != nil {
		return err
	}
	defer pools.Close()
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return fmt.Errorf("discover the identity provider: %w", err)
	}
	client := emberops.New(emberops.Config{BaseURL: base, Service: &emberops.ClientCredentials{
		TokenURL: provider.Endpoint().TokenURL, ClientID: id, ClientSecret: secret, Scope: audience}})

	orgID, ownerID, err := soleOwner(ctx, pools)
	if err != nil {
		return err
	}
	az := authz.New(pools.Web)
	actor, err := az.SubjectsFor(ctx, orgID, ownerID)
	if err != nil {
		return fmt.Errorf("the acting principal for companies made from EmberOps: %w", err)
	}
	opt := crmsync.Options{Write: *write, Remove: *remove, Show: *show, Deals: *deals, Prospects: *prospects, Timeline: *timeline,
		HearthURL: os.Getenv("HEARTH_PUBLIC_BASE_URL"), OpsURL: site, TimeZone: os.Getenv("CRM_SYNC_EMBEROPS_TIMEZONE"),
		Companies: companyMaker{svc: crm.New(pools.Web, az), org: orgID, actor: actor}}
	for _, s := range strings.Split(*only, ",") {
		if s = strings.TrimSpace(s); s != "" {
			opt.Only = append(opt.Only, s)
		}
	}
	rep, err := crmsync.New(pools.Web, client, orgID).Run(ctx, opt)
	rep.Print(os.Stdout)
	if err != nil {
		return err
	}
	if rep.Failed() {
		return errors.New("some records could not be written; run it again, it is safe to repeat")
	}
	return nil
}

// companyMaker makes a company the way the CRM's own form does: its owner, its sales sharing and its created event.
type companyMaker struct {
	svc   *crm.Service
	org   string
	actor core.Subjects
}

func (m companyMaker) MakeCompany(ctx context.Context, name, kind string) (string, error) {
	c, err := m.svc.CreateCompany(ctx, m.org, m.actor, crm.Company{Name: name, Kind: kind})
	if err != nil {
		return "", err
	}
	if err := m.svc.Share(ctx, m.org, m.actor, c.Ref()); err != nil {
		return c.ID, fmt.Errorf("share the new company with sales: %w", err)
	}
	return c.ID, nil
}
