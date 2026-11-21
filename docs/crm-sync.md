# CRM sync to EmberOps

Hearth owns companies, contacts, types, tags, deals, and the timeline. EmberOps
gets a copy of what accounting needs, so an invoice can be raised against a real
Customer or Supplier. It is one command, `hearthctl crm-sync`, and it talks to
EmberOps only through the `emberops_hearth.api.upsert` addon, keyed on
`hearth_id`.

## What goes across

* A company with a type whose ERP role is customer becomes a Customer, one with a
  supplier role becomes a Supplier. A company that is both is both, under the same
  `hearth_id`.
* A company with no role, such as a prospect or one nobody has typed, does not go.
* A contact goes only with a company that did, and a company that is a Lead because of a deal sends its
  people too, as Contacts linked to the Lead.
* Types and tags travel as Frappe tags ("Type: Client", then the free tags).
* Taking the role away disables the EmberOps record, unless accounting has already
  used it (an invoice points at it), in which case it is kept and the report says so.
* The one thing that comes back is a type: a Customer or Supplier EmberOps has for a
  company that lacks the type adds that type here. It never removes one.

## Both ways, one place

Every EmberOps record the sync sends carries a link back to its Hearth page (`hearth_url`) and the name of its
Hearth owner (`hearth_owner`), and a Customer, Supplier or Lead also carries the company's website. After each
run Hearth keeps the EmberOps page of every record it sent (`external_refs`, source `emberops-record`), and its
company, contact and deal pages show an "In EmberOps" link to it. A dry run records nothing.

A record that has not changed since the last run is not sent again (`same` in the report), so a CRM of thousands
of records costs a handful of writes an hour, and EmberOps' history shows real edits only.

A Customer or Supplier made in EmberOps with no Hearth record behind it comes the other way: the Hearth company
with that name gets the Client or Vendor type, or, when there is none, a new company is made (shared with sales,
like one made on the form). The next run links the two by name. A name that matches two Hearth companies is a
conflict and is left. A disabled party is ignored. This is the second thing that comes back, after the type: EmberOps
owns whether a Customer or Supplier exists.

When saving a Lead, EmberOps makes a Contact of its own. That Contact is found (by email, or by name among records
the sync's own EmberOps user made) and taken over rather than duplicated, and from then on follows Hearth.

## Prospects

`hearthctl crm-sync -prospects` (and `crmSync.prospects: true` in the chart) sends every company with no accounting
role as a Lead, and its people as Contacts linked to that Lead, so the whole CRM can be found from EmberOps.
Customer stays what accounting needs. A company nobody has named stays out, as everywhere. The chart turns it on
(`crmSync.prospects: true`); the command line flag is off unless given.

The first run makes thousands of records one at a time, and a run that stops is simply run again: every record is
keyed on its Hearth id, so nothing is made twice, and the Leads already in EmberOps are read once in a single list
and not one call each. After that a run sends only what changed (`same` in the report). EmberOps' own switch that
makes a Contact for every saved Lead is turned off by its setup (EmberOPS 37), so the only Contacts are Hearth's people.

## Notes and calls

`hearthctl crm-sync -timeline` (and `crmSync.timeline: true` in the chart) sends each note and call on Hearth's
timeline as a Comment on the matching EmberOps record: a company's Customer (else its Supplier, else its Lead),
a person's Contact, a deal's Opportunity. A note about several records is one Comment on each, keyed
`<activity id>:<record id>`, so it is never sent twice. One way: Hearth owns the text, a Comment edited in EmberOps is
put back (and reported), and one for an activity removed in Hearth is left. The Comments EmberOps has are read in one
list and only a new or changed one is sent. If EmberOps refuses the first Comment, because it has not been updated
to carry a `hearth_id` on Comment (EmberOPS 1.2.2), the pass stops with one line.

## People typed into EmberOps

The way back for people. An EmberOps Contact with no Hearth id, that the sync did not make (EmberOps makes one when it saves a Lead, and those are
the sync's own), and that holds an email or a number, is looked up in Hearth by email and then by phone (E.164, the rule the phone lookup uses). A match
by email is linked the way every pre-existing record is: its `hearth_id` is set and nothing else, and it stays EmberOps' own. A match by number alone is left
as it is, since there is nothing to match the two on. With no match the person is made in Hearth, shared with sales, under the Hearth company of the Customer, Supplier or Lead
the Contact is linked to when that company has a Hearth id (no company otherwise), and the next run links them by email. A Contact with neither an email nor a
number is left alone. The dry run says `would create contact` for each. This is what puts a person somebody typed into EmberOps in front of the phone, which reads Hearth.

## The phone's calls as Call Logs

With `-timeline` on, every call the phone system logged (an activity with `external_refs` source `phone`) also goes to EmberOps as a
Call Log, ERPNext's own telephony document, keyed on the activity id (`hearth_id`, and `id` is `hearth-<activity id>`). Type is Incoming or
Outgoing, status is Completed for an answered call and No Answer for any other, `from` or `to` is the number on the first line of the activity body
(`Number: +12165550142`), duration is in seconds, and it is linked to the Contact and to the company's Customer, Supplier or Lead, as far
as EmberOps has them. The start and end are written in the EmberOps site's time zone, `CRM_SYNC_EMBEROPS_TIMEZONE` (America/New_York when unset), since
a Frappe Datetime has none. Hearth owns the text, a Call Log is never deleted, and a call a person wrote on the timeline by hand stays a Comment only.
The EmberOps addon (`emberops_hearth`, release 1.2.5 or later) must carry `hearth_id` on Call Log and let the `Hearth CRM Sync` role write it, or the
pass stops with one line.

## Suppliers need a tax ID

EmberOps runs the US compliance pack: it will not save a Supplier without `us_ein`
(and `us_w9_on_file`, which is staff's to attest, not Hearth's). A company's optional
tax ID in Hearth is sent as `us_ein` when it is there. A vendor without one is
reported as "needs an EIN" and its contacts wait; a Supplier EmberOps still refuses
is reported as "needs fields". Neither fails the run.

## Reading it back

`hearthctl crm-sync -only ID -show` reads the Customer, Supplier, and Contacts
EmberOps holds for that company and prints what is stored.

## What EmberOps already has

Before anything is written the sync reads every Customer, Supplier, and Contact in
EmberOps. A company whose name matches a Customer or Supplier that has no
`hearth_id` is linked: its `hearth_id` is set and no other field is touched, so
nothing EmberOps owns is overwritten and nothing is duplicated. The link is recorded in Hearth (`external_refs`, source `emberops-linked`), so it survives every rerun, and a linked record stays EmberOps' own: only fields EmberOps has empty are filled, tags are merged as a union, a non-empty value that differs from Hearth's (`customer_name` included) is reported as a difference and left alone, and the sync never disables or deletes it. Records the sync created itself follow Hearth. A contact is matched
the same way by email address. Two matches, or a match that already belongs to a
different Hearth record, is reported as a conflict and left alone. The dry run
prints every one of these as "would link" or "conflict".

A company with no real name, such as "unnamed 41643296275" from the Twenty import,
is skipped with its contacts and flagged in the report until somebody names it.

## Running it

Dry run first. It prints what it would do and writes nothing anywhere.

    deploy/jobs/crm-sync.yaml with ARGS ["crm-sync"]

Then `-write`. `-only <company id>` limits a run to named companies, and
`-only <id> -remove` takes a proof company back out of EmberOps again. A remove
with no company named is refused.

## One owner per field

Hearth owns what it sends. EmberOps owns what only accounting knows. The sync may
put only these fields on an EmberOps record (`internal/crmsync/owned.go`), and a
document that carries anything else is refused in the run and shown as a failed
record, so a new mapping cannot reach for a credit limit by accident.

| Doctype | Fields Hearth sends |
|---|---|
| Customer | name, type Company, company kind, domain, notes, disabled, tags, website, Hearth link, Hearth owner |
| Supplier | the same, plus `us_ein` from the company's tax ID |
| Contact | first and last name, designation, notes, email and phone rows, the link to the Customer, Supplier or Lead, Hearth link, Hearth owner |
| Lead | lead name, company name, email, tags, website, Hearth link, Hearth owner |
| Opportunity | opportunity from, party, title, status, sales stage, expected closing, amount, currency, Hearth link, Hearth owner |

Never sent, and never read back: credit limit, payment terms, tax category and
withholding, price list, accounts, customer group, territory, sales team, bank and
payment mode. They are accounting's, and Hearth shows a link to the record, not the
numbers. The one thing that comes back is the existence of a Customer or Supplier,
which adds a type here and never removes one.

## The overwrite report

Every field Hearth owns is sent each run, so a value somebody changed in EmberOps
(or one that changed in Hearth since the last run) is replaced. The run lists each
such field before and as it does it: `overwrite` (or `would overwrite` in a dry
run), with what EmberOps has and what Hearth sends. A record EmberOps owned before
the link is never overwritten at all; there it only fills what is empty and
reports differences, as before.

## Deals

Off by default. `hearthctl crm-sync -deals` (and `crmSync.deals: true` in the chart)
also sends Hearth's deals:

* A deal on a company that is a Customer in EmberOps becomes an Opportunity from
  that Customer.
* A deal on any other company becomes an Opportunity from a Lead, and the Lead is
  made first, only for a company that has a deal. Companies with no deal never become
  Leads.
* A deal with no company waits (an Opportunity needs a party). A deal removed in
  Hearth leaves its Opportunity as it is, and the report says so.
* Stages: new is Open at Prospecting, screening is Open at Qualification, meeting is
  Open at Needs Analysis, proposal is Open at Proposal/Price Quote, customer and won
  are Converted, lost is Lost, no response is Closed. The sales stage names are
  ERPNext's standard ones; EmberOps owns that list, so the map is checked against the
  real site before the first write (UNVERIFIED until then).
* The deal stays Hearth's. A stage changed in EmberOps is not read back; the next run
  reports it as an overwrite.

It needs EmberOps to accept Lead and Opportunity from the `Hearth CRM Sync` role,
which is a change in EmberOps' own repository. Until that lands, leave it off.

## The switch that stays off

`HEARTH_EMBEROPS_CRM_PARTIES` defaults to `postgres`. With `HEARTH_BACKEND=emberops`,
companies and contacts stay in Hearth's database and the sales side stays on. Setting
it to `emberops` moves them into EmberOps and turns the sales side off, which is the
old behavior and an explicit choice. This sync is meant to run beside the default.
