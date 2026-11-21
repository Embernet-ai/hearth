# Google Workspace

Hearth can use a Google Workspace domain for sign-in and for four connectors, all of them read and write: calendar, contacts, mail and files, plus chat notifications into Google Chat. It is meant for a self-hosted Hearth. Every connector is off until an administrator turns it on, and every connection is one person's own: Hearth works in a person's mailbox, calendar, address book and drive as that person, and never uses a service account that reaches the whole domain.

## Create the OAuth client

In the Google Cloud console, in a project for Hearth:

1. Enable these APIs: Google Calendar API, People API, Gmail API and Google Drive API.
2. Under Google Auth Platform, set the audience to Internal, so only your Workspace domain can use the app.
3. Add the scopes below to the data access list, only the ones for the connectors you will turn on.
4. Under Clients, make an OAuth client of type Web application with both redirect URIs, exactly, with no trailing slash:
   - `https://hearth.example.com/auth/callback` for sign-in
   - `https://hearth.example.com/integrations/google/callback` for the connectors

| Connector | Scope |
|---|---|
| Sign-in | `openid`, `email`, `profile` |
| Calendar | `https://www.googleapis.com/auth/calendar` |
| Contacts | `https://www.googleapis.com/auth/contacts` |
| Mail | `https://mail.google.com/` |
| Files | `https://www.googleapis.com/auth/drive` |
| Chat notifications | none |

`mail.google.com` and `drive` are restricted scopes. An app whose audience is Internal may use them for your own domain without Google's verification.

## Install

```yaml
oidc:
  enabled: true
  provider: google
  googleHostedDomain: example.com
  clientId: <client ID>
google:
  enabled: true
encryption:
  enabled: true
```

The client secret goes in your existing secret under `OIDC_CLIENT_SECRET` and `GOOGLE_CLIENT_SECRET`, and the client ID under `GOOGLE_CLIENT_ID`.

## Sign-in

With `oidc.provider: google` Hearth requires the signed token's `hd` claim to equal your domain, and the address to be verified. Google sets `hd` only for a Workspace account, so a personal Gmail account has none and is refused. The `hd` parameter that narrows Google's account chooser is only a hint to the person; the claim is what is checked.

Google's sign-in token carries no groups, so there is no group-to-role mapping for Google sign-in. People get capabilities from an owner or a super admin in Hearth, and the allowed-domain list and the hosted domain decide who may sign in at all. For teams that follow Google groups, see [groups-sync.md](groups-sync.md).

## Turn the connectors on

1. Sign in as the owner or a super admin and open Settings, Connectors.
2. Under Google Workspace, enter the primary domain and choose Connect. A person's Google account has to belong to that domain to connect.
3. Turn on the connectors you want: [calendar](connector-calendar.md), [contacts](connector-contacts.md), [mail](connector-mail.md), [files](connector-files.md) and [chat notifications](connector-chat.md).
4. Each person opens the same page, chooses Connect my Google Workspace account, and ticks what they want.

## Check it on your own domain

1. Sign in with a Workspace account. Expect to be signed in. Try a personal Gmail address and expect it to be refused.
2. Connect from Settings, Connectors with the calendar ticked. Expect Google's consent screen listing the calendar.
3. Make an event in Google Calendar. Choose Sync now. Expect it on the Hearth calendar.
4. Make an event in Hearth. Sync. Expect it in Google Calendar, with a Meet link if you turned on links.
5. Share a calendar with a second person, sync it into a department on the Connectors page, and expect the people in that department to see its events in Hearth.
6. Open Mailbox. Expect your inbox, open a message with its whole body, reply, and expect the reply in Gmail's Sent.
7. Make a contact in the CRM and sync. Expect it in Google Contacts, and the reverse.
8. Open Cloud files, upload a file, rename it, move it and delete it, and expect each in Drive.
9. Disconnect, and expect the page to show you as not connected.
