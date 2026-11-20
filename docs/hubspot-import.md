# HubSpot delta import

`hearthctl hubspot-import` brings what was created or changed in HubSpot since the last import into Hearth's CRM:
companies, contacts, deals, notes and calls. The EmberOps sync carries it on from there like any other CRM change.

    hearthctl hubspot-import -since 2026-09-01T00:00:00Z     dry run, writes nothing
    hearthctl hubspot-import -apply                          write it and remember where each object was read to

It reads HubSpot with a private app token (`HUBSPOT_TOKEN`) and writes only through Hearth's CRM service, so every record
gets its grants, its event, a share with the Sales team if it is new, and an audit row (`crm.hubspot_import.create`,
`.update`, `.link`).

## Where it starts

Each object keeps its own high-water mark (`hubspot_import_state`), the newest HubSpot change time an applied run read.
The next run asks HubSpot only for changes at or after it, so a record changed at the very instant of the mark is read
again, which is harmless. The first time there is no mark and `-since` says where to start (when the last import ran).
A dry run never moves a mark, and neither does an apply that had a failure. `-force-since T` reads from `T` even where a
mark exists (the mark never moves back). It is how a field that could not be filled earlier gets filled later, for example
owners once the owner map exists.

The report says, per object and field, how many values fill a blank and how many replace one, and lists the first
replacements, so a run about to touch thousands of records shows what kind of change it is before anyone says go.

## What it matches, and what it will not do

A HubSpot record is matched to Hearth by its HubSpot id: first a record this importer made (`external_refs`, source
`hubspot`), then one the first HubSpot import carried through Twenty (source `twenty`, whose ids are derived from the
HubSpot id). Failing that, a company is matched by web address and a contact by email address, and only when the Hearth
record is not already some other HubSpot record. A match is linked and brought up to date. Nothing is created twice.

* HubSpot having no value for a field never clears one.
* A field somebody edited in Hearth is never overwritten. After a run, the importer keeps a snapshot of what it left in
  each field (`hubspot_import_records`). A later run updates a field only while Hearth still holds that snapshot value.
  A record the first import carried across has no snapshot yet: if it has not been changed in Hearth since that import
  (`-baseline`, when it finished), HubSpot may update it, and otherwise only empty fields are filled. A field left alone is
  listed in the report as a conflict.
* A company named only by its web address (or "(unnamed ...)") counts as unnamed, and HubSpot's name replaces it.
* A web address another company holds stays on the website of the second company, as before.
* Nothing is deleted. A record deleted in Hearth stays deleted.
* A contact or deal pointing at a company Hearth does not have causes that company to be read by id and imported first.
* Owners map to Hearth accounts by email. The token needs HubSpot's owners scope to read them. Without it, `-owner-file`
  (lines of `HUBSPOT_OWNER_ID,email`) supplies the map. Records whose owner has no account are left unowned and counted by
  owner id, and a later run with `-force-since` fills them once a map exists.
* A record's primary company (and a deal's primary contact) is the one HubSpot marks primary, not the first it lists.
* Notes and calls already on the timeline are left as they are, since the timeline is append only. HubSpot meetings and
  tasks are counted in the report and not carried.
* A new company a deal points at is typed from its deals (client for a Customer or Won stage, otherwise prospect), the
  same evidence the first import used. A company already in Hearth is never retyped.
* Deal stages use the first import's stage map. A stage it does not know is reported and left as it is.

## Unsubscribes and bounces

HubSpot knows who asked not to be emailed and whose address bounced. The import reads three contact properties for it
(`hs_email_optout`, `hs_email_hard_bounce_reason_enum`, `hs_email_bad_address`) and writes Hearth's own opted-out flag on the
contact: reason `unsubscribed` for an unsubscribe (the person's choice wins when both hold), `bounced` for a hard bounce or a bad
address. Each one that is newly opted out gets an audit row, `crm.hubspot_import.opt_out`. The properties come with the contacts
scope the import already uses, so no extra scope is needed. An opt-out is never cleared: a contact already opted out in Hearth is
left as it is and counted as already opted out. The report line `do not email` says how many contacts HubSpot flags and how many
a dry run would opt out. Only contacts changed in HubSpot since the mark are read, so a first pass over everyone HubSpot flags is
`-force-since` from the start. The sync to Mautic and the one to EmberOps both carry the flag on from there.

## Running it on the cluster

Run it as a one-off Job on the live image, with `HUBSPOT_TOKEN` supplied from a Secret and the database settings copied
from the Hearth Deployment, dry run first. `-migrate` applies migration 61 (the importer's own tables) when the image
being run is newer than the running server; a dry run copes without the tables.
