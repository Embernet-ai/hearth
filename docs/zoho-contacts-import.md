# Importing a Zoho CRM contacts export

```
hearthctl zoho-contacts-import -file Contacts.xlsx -url https://hearth.example.com -owner PERSON_ID -dry-run
HEARTH_API_TOKEN=hearth_api_... hearthctl zoho-contacts-import -file Contacts.xlsx -url https://hearth.example.com -owner PERSON_ID -manifest ids.jsonl
```

Reads the `.xlsx` the Zoho Contacts module exports and adds it through Hearth's own
CRM API, so it needs an API token with the write scope (minted on the Integrations
page) and nothing else: no database credentials, and it works against any
deployment. `-owner` is the id of the person everything is assigned to, the part
after `/people/` on their page.

## What moves

| Zoho | Hearth |
|---|---|
| Account Name (one per distinct account id) | Company, kind `customer`, notes `Zoho account id: ...` |
| First Name, Last Name | Contact name (just the last name when there is no first) |
| Email | Contact email, lowercased |
| Title, Phone | Contact title, phone |
| Home Phone, Mobile | The contact's extra phones (a number that repeats Phone is not sent again) |
| Every other column with data | The contact's Notes, one `Label: value` line each: Record Id, Zoho owner, lead source, department, industry, fax, mailing and other address, opt out and unsubscribe fields, created and modified by and time, last activity, and so on |

Contact Name is First and Last put together and is not kept; the run counts the rows
where it disagrees. Any column that carried data and has no place at all is named in
the report, so nothing is dropped quietly.

## Safe to run twice, and never a duplicate

Every create carries the Zoho id as its external id (`zoho-crm`), so a second run
answers with the record that exists and changes nothing. Before writing, the importer
reads what the instance already holds: a company with the same name (ignoring case)
or a contact with the same email is used as it is, counted as existing, and never
edited or created again.

## Taking it back out

`-manifest` writes one line of ids per record made or found, with no personal data.
`hearthctl crm-delete -company ID,ID` removes the companies it made with their
contacts, and `-contact ID,ID` removes contacts that have no company.

## Pace

8 requests a second by default (`-rate`), under the API's own limit, and a 429 or a
5xx is waited out and retried.
