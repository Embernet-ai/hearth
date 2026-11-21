# Contacts connector

Keeps a person's address book at Google Contacts or Outlook and the Hearth CRM the same, in both directions. Available for Google Workspace and Microsoft 365. Off until an administrator turns it on.

## What syncs

Each contact with a name or an address comes in as a CRM contact with its name, address, phone and job title, and each contact a person makes in the CRM goes to their address book. If an address is already in the CRM, Hearth links the two and changes nothing at first, because the existing record is the better source. A company is attached when the contact's company matches one the person can already see, name for name. Hearth does not make companies from free text.

| Where it changed | What happens |
|---|---|
| A new contact at the provider | A new CRM contact |
| A new CRM contact, made by the person | A new contact at the provider |
| Changed at the provider only | Hearth takes the change |
| Changed in Hearth only | The provider takes the change |
| Changed on both sides since the last run | The provider's version wins, and the log says so |
| Deleted at the provider | Deleted in the CRM. It is a soft delete, so the CRM can restore it |
| Deleted in the CRM | Deleted at the provider |

## Who sees them

Contacts that come in are imported as the person who connected, so they are that person's own in the CRM. The CRM's own sharing decides who else sees one. Connecting an address book does not hand it to the company.

## Permissions

| Provider | Permission |
|---|---|
| Google Workspace | `https://www.googleapis.com/auth/contacts` |
| Microsoft 365 | `Contacts.ReadWrite` (delegated) |
