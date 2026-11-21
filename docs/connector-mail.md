# Mail connector

Links messages in a person's Gmail or Outlook mailbox to the people already in the Hearth CRM. Read only. Available for Google Workspace and Microsoft 365. Off until an administrator turns it on.

## What is kept

Hearth reads recent mail, the last fourteen days at first and then what is new, and keeps a message only when its sender or a recipient is a contact the person can see in the CRM. For those it keeps the sender, the recipients, the subject, a preview of up to 240 characters, the time, and the link back to the message at Google or Microsoft. It never keeps a message body or an attachment, never sends, moves or deletes anything, and drops a stored link after ninety days.

## Who sees it

The linked messages show on the contact's page, to the person whose mailbox they came from and to nobody else. A colleague who can see the same contact does not see mail from somebody else's mailbox. Disconnecting deletes the person's linked messages.

## Permissions

| Provider | Permission |
|---|---|
| Google Workspace | `gmail.metadata`, which cannot read a body |
| Microsoft 365 | `Mail.Read` (delegated) |

Microsoft has no narrower delegated permission that returns a preview, so `Mail.Read` is the least that does the job. Hearth asks for the subject, sender, recipients, preview, time and link only, and never the body.
