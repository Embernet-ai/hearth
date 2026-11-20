# Connect an assistant to Hearth

Hearth serves an assistant connector at `https://hearth.example.com/mcp`, over streamable HTTP. A person adds it as a
custom connector in their assistant, signs in as themselves through your sign-in provider, and every tool runs with
exactly the rights that person has on the website. Nothing is shared and there is no token to paste.

## Who can connect

A person needs three things, and Hearth checks each one on every request:

1. A sign-in with the role set in `mcp.requiredRole`, if you set one. Without it the endpoint answers 403 before it
   looks anyone up.
2. A Hearth account linked to that sign-in. Sign in to Hearth in a browser once. A person who has never signed in is
   refused with "not provisioned", and Hearth does not make an account from a token.
3. A token whose audience is `hearth-mcp` (or the audience you set in `mcp.audience`). A token for any other service
   is refused with 401.

What a person may do after that comes from their Hearth grants and teams, the same objects the website uses. Asking
for a task they cannot see answers "no such entity", and an empty list means they cannot see anything, not that
nothing exists.

## Add it in your assistant

1. In your sign-in provider, register a public client for the assistant. It uses PKCE and has no client secret. Allow
   the callback address your assistant documents for custom connectors.
2. Add a client scope that puts the audience `hearth-mcp` into the token, and give the client that scope.
3. In the assistant, add a custom connector named Hearth with the server URL `https://hearth.example.com/mcp`, and
   enter the client ID you registered.
4. Connect, and sign in when your provider asks.

## How the sign-in works

The first request has no token and gets a 401 whose `WWW-Authenticate` header carries `resource_metadata`, the address
of the protected resource document (RFC 9728) at `/.well-known/oauth-protected-resource/mcp`. That document names your
provider as the one authorization server and lists the scopes to ask for, including the one that puts the audience in
the token. The client reads the provider's discovery document, sends the person through it with PKCE S256, and
exchanges the code for an access token and a refresh token.

The provider is the only place a password is typed. Hearth verifies the token's signature, issuer, expiry and audience,
then looks the subject up in its own identity table.

## Agent tokens

The same endpoint also takes a Hearth agent token (`hearth_agent_...`) for unattended automation. Those carry their own
principal and grants, and their writes may be suggestions. They are made on the Agents page.

## Tools

Search, read, list, create and update for documents, plus the CRM tools in [crm-api.md](crm-api.md). Each call writes
one log line with who, which client, which tool and how it went.
