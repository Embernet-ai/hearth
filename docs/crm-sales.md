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

## The daily work

The sidebar group is **CRM**: Contacts, Companies, Deals, Follow-ups, Forecast, Sales reports, and Unassigned. Team chat is its
own group, Chat. People who do not work sales keep Contacts and Companies and see the records their grants already show them;
the rest of the group is offered to the Sales team and to super admins only, and every address behind it answers 404 to anyone else.

- **Everybody's by default.** The board, the follow-up queue, the forecast and the report start with every deal and every owner.
  A filter narrows them to one person, "Mine" included, and it is never what a page opens with. A super admin sees every record
  whether or not they are on the Sales team.
- **Follow-ups** (`/crm/followups`). A follow-up is a to do, a call, an email, or a meeting, and the queue filters by kind. Push one out with +1d, +3d or +7d (from its due day, or from today when it is late), tick many and mark them done together. What the team owes people: a title, a day, who has it, and the company, person or deal it is
  about. Add one from any record, see them on the record, and work the queue by overdue, today, or the next seven days. The person who
  has one is told when it is due within a day, once a day, in their inbox, and the notice opens the record.
- **Timeline.** Filter a record's timeline to notes, calls, meetings or emails with one click, and edit any entry (what it says, when it happened, a call's direction, outcome and length). The author may, and so may anybody who can edit every record the entry is about. Its kind and what it is about never change, and EmberOps's copy of the Comment follows the edit on the next sync.
- **Custom fields** (`/crm/properties`). An admin adds fields for people, companies or deals: text, number, currency, date, yes or no, a choice, a web address, an email, a phone number. They show as a section on every record of that kind, anybody who can edit the record fills them in, and an empty value clears one. Filter the people, companies and deals lists by a field (pick the field, type the value, or `*` for any value; a text field matches inside the text, every other kind matches the whole value, any case), and every CSV export adds a column per field and follows the same filter. They use the same property tables as documents (no migration), they stay in Hearth, and the EmberOps sync never sends them. A choice is matched case-blind and stored as written in the list.
- **Deal fields.** A deal shows its close date and how many days it has sat in its stage. A lost deal carries why it was lost.
- **Forecast** (`/crm/forecast`). Open deals by the month they are expected to close, with a bucket for those past their date and
  those with none. Every stage has a chance of closing (New 10, Screening 20, Meeting 40, Proposal 60, Customer 90), and the
  weighted value is the amount times that chance. Won this month sits beside it. A CSV of the lot.
- **Sales reports** (`/crm/reports`). For any period: the pipeline by stage, what was won and lost, win rate, average won deal, days
  from made to won, each person's pipeline and results, what each person logged (notes, calls, meetings, emails), and deals made by month.
- **Bulk change.** Tick rows on the deals table, the companies list or the contacts list and set an owner, move deals to a stage,
  or add a tag to companies. Each record goes through the same write a single edit uses, so a record the person may not edit is
  left alone and named, and every change reaches EmberOps through the sync like any other.
- **Email and meetings from a record.** Through the person's own connected Google or Microsoft account (Settings, Connectors), send
  an email or book a meeting with a Meet or Teams link, with the invitation mailed to the other side. Both land on the timeline of the
  record and of the company or person it belongs with. Meetings and emails can also be logged after the fact, beside notes and calls.

## The tools beside the CRM

Hearth does not rebuild what EmberOps, Mautic, Formbricks and Cascade already do. It shows their data on the record and reaches into
them where a salesperson needs to.

- **EmberOps quotes and call logs.** A deal shows the quotations on its Opportunity, a company the quotations for its Customer or Lead,
  and a person the calls EmberOps logged for their numbers or their Customer. **New quote in EmberOps** opens a new Quotation there with
  the party and the opportunity filled in, because building a quote (items, price lists, the CPQ configurator) is EmberOps's job. The
  sales report adds what was quoted in the period. Hearth reads these with its own EmberOps service account, so the Sales team and super
  admins see them whatever their EmberOps permissions are.
- **Mautic.** What Mautic recorded for a person (sent, opened, clicked, forms, unsubscribed, bounced) is on their timeline. Their page
  now also lists their Mautic segments and campaigns, and a salesperson adds or removes them by hand. Segments the marketing sync keeps
  ("Hearth: ...") are shown and cannot be changed there, because the next sync would undo it. Tick people on the contacts list and enrol them in a segment or a live campaign in one go; anyone the marketing sync has not sent to Mautic yet is counted and left for the next sync, and anyone you cannot edit is left out.
- **Formbricks.** A person's page lists the answers they gave on any form, matched on their email addresses.
- **Cascade.** Companies, people and deals publish created, updated and deleted events to webhook receivers, and a follow-up added or
  finished is an update of the record it is about (`payload.followup` is `created`, `done` or `reopened`). A Cascade flow starts from
  a webhook (Settings, API, webhooks), reads the record from `/v1`, and writes back through `/v1`, so every change it makes goes through
  the same checks and reaches EmberOps through the sync like any other.
- **Duplicates.** `/crm/duplicates` finds people and companies that look like one (same name at the same company, the same number, one
  person's address among another's, the same company name or website). Merge keeps one record and folds the rest into it: deals, notes,
  follow-ups, people, tags and favorites move across, blanks are filled, email addresses and numbers become the survivor's extra ones,
  and an unsubscribe is never undone. Each merge is recorded, with what the removed record held.

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
  is rewritten. Migration 00068 records merges. Migration 00067 adds meetings and emails to the timeline, a deal's lost reason and the time it
  entered its stage (kept by a trigger, so every writer is covered), and the follow-ups table.
- Deals, notes, and calls are not in global search yet, only on their own pages.
