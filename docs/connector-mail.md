# Mail connector

A person's Gmail or Outlook mailbox inside Hearth, read and write, as that person. Available for Google Workspace and Microsoft 365. Off until an administrator turns it on.

## The mailbox

Mailbox in Settings opens the person's inbox, sent mail, archive or all mail, with search in the provider's own syntax. Any message the person can see opens with its whole body. From there they can:

- send a new message, with To, Cc and Bcc
- reply, or reply to everybody, in the same thread
- forward a message to other people with a note above it
- archive it, label it (Gmail labels, Outlook categories), mark it read or unread, and delete it to the bin

Everything passes straight through to Google or Microsoft as the person, so a reply appears in their own Sent folder and nothing is kept in Hearth. A message with attachments lists their names and links back to the message to download them. A forward carries the text of the message, and attachments stay with the original.

## Mail on a contact

Hearth also reads recent mail, the last fourteen days at first and then what is new, and links each message to the people in the CRM it names. The whole message is kept for those, and shows on the contact's page to the person whose mailbox it came from and to nobody else. A colleague who can see the same contact does not see mail from somebody else's mailbox. A stored message is dropped after ninety days, and disconnecting deletes them all.

## Permissions

| Provider | Permission |
|---|---|
| Google Workspace | `https://mail.google.com/` |
| Microsoft 365 | `Mail.ReadWrite` and `Mail.Send` (delegated) |
