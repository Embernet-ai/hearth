# Contacts connector

Imports a person's address book from Google Contacts or Outlook into the Hearth CRM, and keeps it current. Available for Google Workspace and Microsoft 365. Off until an administrator turns it on.

## What it does

Each contact with a name or an address becomes a CRM contact with its name, address, phone and job title. If the address is already in the CRM, Hearth links the two and changes nothing, because the existing record is the better source. A company is only attached when the contact's company matches one the person can already see, name for name. Hearth does not make companies from free text.

The sync only adds and updates:

| Where it changed | What happens |
|---|---|
| A new contact at the provider | A new CRM contact |
| A contact changed at the provider, not edited in Hearth | Hearth takes the change |
| Edited in Hearth since the last sync | Hearth's edit is kept |
| Deleted at the provider | The CRM record stays, with its deals and history |
| Deleted in Hearth | It is not made again |

## Who sees them

The contacts are imported as the person who connected, so they are that person's own in the CRM. The CRM's own sharing decides who else sees one. Connecting an address book does not hand it to the company.

## Permissions

| Provider | Permission |
|---|---|
| Google Workspace | `contacts.readonly` |
| Microsoft 365 | `Contacts.Read` (delegated) |

Hearth never writes back to an address book.
