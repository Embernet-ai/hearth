# The sales side of the CRM

Contacts and companies were built to answer a maintenance question: who fixed the
compressor, and what is their number. Fireball also sells, so the same tool now
holds the pipeline.

## What is there

- **Deals** (`/deals`): a board with one column per stage (New, Screening, Meeting,
  Proposal, Customer, Won, Lost, No response), or a sortable, filterable table.
  Drag a card to change its stage. Every card also has a stage picker, so it works
  without a mouse. Each column shows its count and its total.
- **Owners.** Companies, contacts, and deals carry an optional owner. An owner is a
  label and a filter. It never decides who can see a record.
- **Unassigned** (`/crm/unassigned`): everything nobody owns yet, by type, oldest
  first. Claim a record, or hand it to somebody from the picker. Claimed records
  leave the queue.
- **Timeline.** Notes and calls on any company, contact, or deal, newest first. One
  call can be about a contact, their company, and a deal, and shows on all three.
  Tasks linked to a record show under "Work and assets" on its page.
- **Lists** (companies, contacts, deals): search, owner filter, sort by column,
  fifty to a page, and a CSV of whatever the list is showing, every page of it.
- **Company details:** owner, website, LinkedIn, address, annual revenue. **Contact
  details:** owner, LinkedIn, other emails, other phones. A new company kind,
  `prospect`.

## Types and tags

A company can be several things at once: client, prospect, vendor, partner,
contractor, manufacturer, or whatever an admin adds on the Types page (`/crm/types`).
Free tags sit beside the types for everything that is not worth one. Both are set
on the company page and show as columns in the company list, as filters (a type,
"No type yet", or a tag), and in the CSV.

The older single `kind` stays where it was, so nothing that reads it changes.
Types are the richer answer and are what the list filters on.

Each type can name the EmberOps record it lines up with: a client is a Customer
there, a vendor is a Supplier. That column is how the two systems will be kept in
step; see the sync design in the codex.

A company gets its first types only from evidence. A kind somebody chose becomes
the matching type, a won or customer deal makes a client, any other deal makes a
prospect, and a company with no deal and no kind gets nothing and is found with
the "No type yet" filter.

## Who sees what

Hearth's roles are teams and grants, and the CRM uses exactly those. Every deal,
and everything the importer brings in, is shared with the **Sales** team. People
who work the CRM go in that team and see every record, owned or not. Admins see
everything through the bypass they already have. Somebody outside the team sees
what Hearth's grants already say about them, which for these records is nothing.
An organisation with no team called Sales shares nothing beyond the creator, as
before. New companies and contacts made on the existing forms are shared with the
Sales team the same way once it exists.

## Notes for operators

- Needs Postgres. With `HEARTH_BACKEND=emberops` the pages are not offered, and
  nothing else changes; the existing `crm.Store` interface is untouched.
- Migration 00048 only adds columns with defaults and new tables. No existing row
  is rewritten.
- Deals, notes, and calls are not in global search yet, only on their own pages.
