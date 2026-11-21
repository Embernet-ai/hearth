# Microsoft 365

Hearth can use a Microsoft 365 business tenant for sign-in and for four connectors, all of them read and write: calendar, contacts, mail and files, plus chat notifications into Teams. It is meant for a self-hosted Hearth. Every connector is off until an administrator turns it on, and every connection is one person's own, made with delegated permissions: Hearth works in a person's mailbox, calendar, address book and drive as that person, and never holds an application permission that reaches a whole tenant.

## Register the app in Microsoft Entra

In the Microsoft Entra admin center, under App registrations, make one registration for Hearth.

1. Supported account types: accounts in this organizational directory only (single tenant), or accounts in any organizational directory if one Hearth serves several tenants.
2. Redirect URIs, platform Web, both of these exactly, with no trailing slash:
   - `https://hearth.example.com/auth/callback` for sign-in
   - `https://hearth.example.com/integrations/microsoft/callback` for the connectors
3. Under Certificates and secrets, make a client secret and put its value in your Hearth secret.
4. Under API permissions, add these as Delegated permissions, and only the ones for the connectors you will turn on:

| Connector | Delegated permission |
|---|---|
| Sign-in | `openid`, `profile`, `email`, `offline_access`, `User.Read` |
| Calendar | `Calendars.ReadWrite` |
| Contacts | `Contacts.ReadWrite` |
| Mail | `Mail.ReadWrite`, `Mail.Send` |
| Files | `Files.ReadWrite.All`, `Sites.Read.All` (to find the SharePoint sites a person follows) |
| Chat notifications | none |

`Files.ReadWrite.All`, `Sites.Read.All` and some tenant policies need an administrator's consent. Grant it once for everybody under API permissions, or from Settings, Connectors, Grant for the whole tenant.

## Install

In the chart values:

```yaml
oidc:
  enabled: true
  provider: microsoft
  microsoftTenant: 8f1c2b7e-0000-4a1d-9c3e-5d2f7a6b1c90   # your directory (tenant) ID
  clientId: <application (client) ID>
  allowedDomains: contoso.com
microsoft:
  enabled: true
encryption:
  enabled: true
```

The client secret goes in your existing secret under `OIDC_CLIENT_SECRET` and `MICROSOFT_CLIENT_SECRET`, the application ID under `MICROSOFT_CLIENT_ID`. Encryption has to be on, because a refresh token has to be stored and Hearth will not store one in the clear.

## Sign-in

With `oidc.provider: microsoft` the sign-in goes to your tenant's own authority and checks the `tid` claim in the signed token against `microsoftTenant`. People are matched on the object ID (`oid`), which survives a rename, and never on their address. Entra sends no `email` claim unless you add it, so Hearth falls back to `preferred_username` for the allowed-domain check.

For one Hearth that serves several tenants, set `microsoftTenant: organizations` and list the directory IDs in `microsoftAllowedTenants`. Left empty, any work or school account may sign in, and Hearth logs that at startup. Each token's issuer is checked against its own tenant.

### Groups to roles

`oidc.roleMap` gives a Hearth capability to anybody whose token carries a group or app role:

```yaml
oidc:
  roleMap: "5f0e2c1a-1111-4b6e-9d3f-aaaaaaaaaaaa=audit,Finance-Team=finance"
```

In your app registration, under Token configuration, add the groups claim (security groups) or define app roles and assign them. A group is named by its object ID in the token; an app role by its value. The capabilities are `people`, `agents`, `audit`, `finance` and `time_all`. `super_admin` cannot be given this way, and the account that claimed the installation is never changed. Losing the group takes the capability back at the next sign-in, and a capability somebody granted by hand in Hearth is never taken back.

An account in more than 200 groups gets a pointer in its token instead of the list. Hearth then asks Microsoft Graph for the list as that person, which is why sign-in asks for `User.Read`. If Graph will not answer, that sign-in fails rather than carry on as if the person had no groups.

## Turn the connectors on

1. Sign in as the owner or a super admin and open Settings, Connectors.
2. Under Microsoft 365, enter the directory (tenant) ID and choose Connect. Only accounts in that tenant can connect.
3. Turn on the connectors you want. Each has its own page: [calendar](connector-calendar.md), [contacts](connector-contacts.md), [mail](connector-mail.md), [files](connector-files.md) and [chat notifications](connector-chat.md).
4. Each person opens the same page, chooses Connect my Microsoft 365 account, and ticks what they want. They see what Microsoft asks before anything is granted.

## Check it on your own tenant

Hearth was built and tested against Microsoft's published API shapes and recorded responses. It has not been run against a live tenant by the people who wrote it, so run this once on yours before relying on it:

1. Sign in with a Microsoft account in the tenant. Expect to land signed in, with the account named as in Entra.
2. Connect from Settings, Connectors with the calendar ticked. Expect a Microsoft consent screen listing `Calendars.ReadWrite`.
3. Make an event in Outlook for tomorrow. Choose Sync now. Expect it on the Hearth calendar.
4. Make an event in Hearth. Choose Sync now. Expect it in Outlook, and a Teams link on it if you turned on links.
5. Move the Hearth event. Sync. Expect it moved in Outlook.
6. Open Mailbox. Expect your inbox, open a message with its whole body, reply to it, and expect the reply in Outlook's Sent Items.
7. Make a contact in the CRM and sync. Expect it in your Outlook contacts, and the reverse.
8. Open Cloud files, upload a file, rename it, move it and delete it, and expect each in OneDrive.
9. Disconnect. Expect the Connectors page to show you as not connected.

If a step fails, the Connectors page shows the last error for each connector and each person, in words.
