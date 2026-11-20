# Connect Claude to Hearth

Hearth serves the Model Context Protocol at `https://hearth.example.com/mcp`, over streamable HTTP. A person adds it
as a custom connector in Claude (claude.ai, the desktop app and the mobile apps share it), signs in as themselves
through Vord, and every tool runs with exactly the rights that person has on the website. Nothing is shared and there
is no token to paste.

## Who can connect

A person needs three things, and Hearth checks each one on every request:

1. A Vord sign-in with the `fireball-staff` realm role. Without it the endpoint answers 403 before it looks anyone up.
2. A Hearth account linked to that Vord identity. Sign in to Hearth in a browser once. A person who has never signed in
   is refused with "not provisioned", and Hearth does not make an account from a token.
3. A token whose audience is `hearth-mcp`. A token for any other service in the realm is refused with 401.

What a person may do after that comes from their Hearth grants and teams, the same objects the website uses. Asking
for a task they cannot see answers "no such entity", and an empty list means they cannot see anything, not that
nothing exists.

## Add it in Claude

1. Open Customize, then Connectors, and choose Add, then Add custom connector.
2. Name it Hearth. For the server URL enter `https://hearth.example.com/mcp`.
3. Open Advanced settings. Under OAuth client choose Use your own OAuth client, enter the client ID `hearth-mcp`, and
   leave the client secret empty. It is a public client: Claude sends a PKCE challenge on every sign-in and Vord
   refuses a request without one.
4. Choose Add, then Connect, and sign in with your Fireball account when Vord asks.

Claude's callback is `https://claude.ai/api/mcp/auth_callback`. It is registered on the `hearth-mcp` client, along with
`https://claude.com/api/mcp/auth_callback` (the host Anthropic says it may move to) and the loopback forms Claude Code
uses.

## How the sign-in works

The first request has no token and gets a 401 whose `WWW-Authenticate` header carries `resource_metadata`, the address
of the protected resource document (RFC 9728) at
`/.well-known/oauth-protected-resource/mcp`. That document names Vord's `fireball` realm as the one authorization
server and lists the scopes to ask for, including `hearth-mcp`, the scope that puts the audience in the token. The
client reads Vord's own discovery document, sends the person through Vord with PKCE S256, and exchanges the code for
an access token that lasts five minutes and a refresh token.

Vord is the only place a password is typed. Hearth verifies the token's signature, issuer, expiry and audience, then
looks the subject up in its own identity table.

## Agent tokens

The same endpoint still takes a Hearth agent token (`hearth_agent_...`) for unattended agents. Those carry their own
principal and grants, and their writes may be suggestions. They are made on the Agents page.

## Tools

Search, read, list, create and update for documents, plus the CRM tools in [crm-api.md](crm-api.md). Each call writes
one log line with who, which client, which tool and how it went.
