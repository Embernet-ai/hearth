# The CRM API

Companies, contacts, deals, and the notes and calls on their timelines, under
`/api/v1`, with the same bearer tokens as the documents endpoints
(`Authorization: Bearer hearth_api_...`, minted on the account page). Needs
Postgres: a deployment that keeps its data in EmberOps answers 404 on all of it.

A token acts as the person it belongs to, so the API refuses what the pages
refuse for that person. Sales team members and admins see every record. Anybody
else sees what their grants say, which for CRM records is nothing, and is told
404, never 403, so an id cannot be probed. A read token cannot write.

Unknown fields in a body are refused (400), so a misspelled field is an error and
not a quietly empty record.

## Endpoints

| | |
|---|---|
| `GET /v1/companies` | `q`, `kind`, `type` (a type key, or `none` for no type yet), `tag`, `owner` (a person id, or `none`), `sort`, `desc=1`, `limit` (50, max 200), `offset`. Returns `{companies, total}` |
| `POST /v1/companies` | `name`, `domain`, `kind`, `notes`, `website`, `linkedin_url`, `tax_id` (an EIN, optional), `address {street, city, state, postcode, country}`, `annual_revenue_cents`, `revenue_currency`, `owner_id`, `types` (keys from `/v1/company-types`), `tags` (free text) |
| `GET /v1/company-types` | the type list: `key`, `label`, `erp_role` (customer, supplier, or empty) |
| `GET`, `PATCH /v1/companies/{id}` | PATCH changes only the fields it names, address fields one by one |
| `GET /v1/contacts` | the same filters, plus `email` for an exact, case-insensitive lookup, and `company_id` |
| `POST /v1/contacts` | `name`, `email`, `phone`, `title`, `notes`, `company_id`, `linkedin_url`, `extra_emails`, `extra_phones`, `owner_id`, `marketing_opt_out` (only `true`: it stops marketing email to the person and is never taken back here; the answer shows `marketing_opt_out`, `marketing_opt_out_at` and `marketing_opt_out_reason`, see mautic-sync.md) |
| `GET`, `PATCH /v1/contacts/{id}` | |
| `GET /v1/deals` | `q`, `stage`, `owner`, `company_id`, `contact_id` (the deals a person is the point of contact on), `sort`, `limit`, `offset` |
| `POST /v1/deals` | `name`, `stage` (new, screening, meeting, proposal, customer, won, lost, no_response), `amount_cents`, `currency`, `close_date` (YYYY-MM-DD), `company_id`, `contact_id`, `owner_id`, `notes` |
| `GET`, `PATCH /v1/deals/{id}` | moving a deal is a PATCH of `stage`; `lost_reason` says why a deal was lost. A deal reads back `stage_changed_at`, `closed_at`, `probability_percent` and `weighted_cents` |
| `POST /v1/activities` | `kind` (note, call, meeting or email), `title`, `body`, `occurred_at`, `direction` (inbound or outbound, for calls and emails), `disposition`, `duration_ms`, `targets` (a list of `{type: company, contact or deal, id}`) |
| `GET /v1/{companies,contacts,deals}/{id}/activities` | the timeline, newest first |
| `GET /v1/followups` | `assignee` (a person id, `me`, or `none`), `status` (`open`, `done`, `all`; open by default), `due` (`overdue`, `today`, `week`), `target_type` and `target_id`, `limit`, `offset`. Everybody's unless `assignee` says otherwise. Returns `{followups, total}` |
| `POST /v1/followups` | `title`, `target {type: company, contact or deal, id}`, `due` (YYYY-MM-DD), `assignee_id` (the caller by default), `body` |
| `PATCH /v1/followups/{id}` | `done` (true or false), `due` (an empty string clears it), `assignee_id` |
| `DELETE /v1/followups/{id}` | |
| `GET /v1/crm/forecast` | `owner` (a person id, `me`, or `none`). Open deals weighted by stage chance, in buckets: `overdue`, each month, `none`; per owner; won this month |
| `GET /v1/crm/reports` | `from`, `to` (YYYY-MM-DD, the last 30 days by default), `owner`. Pipeline by stage, won, lost, win rate, average won deal, days to win, owners, activity by person, deals made by month |
| `POST /v1/crm/bulk` | `type` (company, contact, deal), `ids` (at most 500), and any of `owner_id` (empty clears), `stage` (deals), `add_tag`, `add_type` (companies). Returns `{changed, failed}`; a record the caller may not edit is named in `failed` and the rest are changed |
| `GET /v1/crm/stats` | companies, contacts, deals, open deals (not won, lost, or no response), open value in cents, and count and value by stage, over what the caller can see |

The follow-up, forecast, report and bulk routes are for people who work sales (the Sales team) and super admins. A token whose person
does not is told 404 on all of them. A read token can list and read them and cannot write.

## Sending a create twice

Every create takes an optional `external_id` and `source` (default `api`). The
first time a pair is seen it creates the record. Every time after, it answers
**200** instead of 201, with the header `Idempotent-Replay: true` and the record
that already exists, and changes nothing, whatever the new body says. Two requests
with the same pair at the same instant make one record. The pair is remembered per
kind of record, so `contact:42` and `deal:42` do not collide.

Use it for anything that can retry: a webhook delivery id, a conversation id, a
survey response id.

## Find, then create

```
GET  /v1/contacts?email=dana@example.com     -> {"contacts": [], "total": 0}
POST /v1/contacts {"name": "Dana", "email": "dana@example.com"}   -> 201
POST /v1/contacts {"name": "Dana", "email": "dana@example.com"}   -> 409 {"error": "...", "existing_id": "..."}
```

`existing_id` is present only when the caller may see that contact.

## Who the token should belong to

An automation needs a person to act as, and that person decides what it can see
and what it is recorded as having done. Make an account for the automation, put it
on the Sales team, and mint its token with the write scope. Do not use somebody's
own token for a flow that runs unattended.

## Types and tags

A company has `types` (keys, any number) and `tags` (free text). Sending `types` or `tags` on a create or a PATCH replaces that half and leaves the other alone. A key that is not on the list answers 400 before anything is created, so a bad key cannot leave a half-made company behind. Admins edit the list in the web UI.

## The same thing through the assistant connector

Hearth's connector endpoint (`/mcp`, an agent token or a person signed in with Vord)
offers the CRM as tools. Each tool is one route above run through its own
handler with the caller's own subject set, so it sees what the API shows that
caller, refuses what the API refuses, and keeps the same idempotency: a create
that carries `external_id` and `source` twice answers "Already existed, nothing
changed" with the record that exists. Nothing is copied into the connector side.

| Tool | Route |
|---|---|
| `crm_stats` | `GET /v1/crm/stats` |
| `crm_find_contacts` (use `email` to find one person) | `GET /v1/contacts` |
| `crm_get_contact`, `crm_get_company`, `crm_get_deal` | `GET /v1/{contacts,companies,deals}/{id}` |
| `crm_list_companies`, `crm_list_deals` | `GET /v1/companies`, `GET /v1/deals` |
| `crm_timeline` | `GET /v1/{companies,contacts,deals}/{id}/activities` |
| `crm_create_contact`, `crm_update_contact` | `POST /v1/contacts`, `PATCH /v1/contacts/{id}` |
| `crm_create_company`, `crm_update_company` | `POST /v1/companies`, `PATCH /v1/companies/{id}` |
| `crm_create_deal`, `crm_update_deal` (moving a deal is a change of `stage`) | `POST /v1/deals`, `PATCH /v1/deals/{id}` |
| `crm_add_activity` | `POST /v1/activities` |

The write tools are offered only to a caller whose writes land directly: a
person, or an agent token with the write scope on a deployment that is not
suggest-only. A read-only token never sees them. A deployment that keeps its
companies and contacts in EmberOps has no CRM tools, as it has no CRM API.

## Who is calling: phone lookup

`GET /v1/crm/lookup?phone=<number>` answers which contact holds a phone number, for a phone system's caller id.

* The number is normalized to E.164 first. A leading `+` or `00` keeps the country code that was typed; ten digits, or eleven starting with 1, are
  North American (+1). Anything else is ambiguous and is refused with 400 rather than guessed.
* It matches the contact's number and extra numbers, however they were typed ("(216) 555-0142", "216-555-0142", "+1 216 555 0142").
  Migration 00059 holds the rule as `phone_e164()` and a GIN index over it, so every write path (pages, API, importers, syncs) is covered.
* It is scoped to the org and to what the token's person may see, exactly like every list. A read token is enough.
* 200 `{"e164", "matches", "contact": {"id", "name", "title", "company_id"}, "company": {"id", "name"}}`, with the most recently updated contact
  when several hold the number; 404 when nobody the caller may see holds it.
* `GET /v1/contacts?phone=` filters the same way.

### A service's own token

A service (the phone system's caller id and call log bridge) must not borrow a person's token. `hearthctl service-token -as ADMIN -name phone-bridge -team Sales -out FILE`
makes the service its own agent principal, puts it on the Sales team so the CRM's grants show it that team's records and nothing else, and mints it a
token with the write scope, which includes read: a company service reads and writes (the phone system looks callers up, logs each call and keeps
contacts). `-scope read` makes a read-only token for a service that only looks things up. It is idempotent (the agent is found by handle, the team add is a no-op
the second time), a new token is minted each run and the old ones are revoked on the Integrations page. The token goes to `-out`, mode 600, and is never
printed. Only a super admin can run it.

### Calls from the phone system

The phone system logs each call with `POST /v1/activities`: `kind` call, `direction`, `disposition` (answered, missed, no answer), `duration_ms`,
`occurred_at`, `title`, a `body` whose first line is `Number: +12165550142` and `targets` of the contact and its company, and `source` `phone` with
the call's id as `external_id`, so a retry is one activity. With `crmSync.timeline` on, the hourly sync sends every activity with source `phone`
to EmberOps as a Call Log (see crm-sync.md). A caller nobody knows becomes a contact named by their number, made with the same `source` and
`external_id` (`caller:<number>`), so there is one.
