# Chat notifications

Copies a person's Hearth notifications into a Google Chat space or a Microsoft Teams chat they choose. Available for Google Workspace and Microsoft 365. Off until an administrator turns it on. Unlike the other connectors it asks the provider for no permissions, because it uses an incoming webhook.

## Set it up

A person makes a webhook in the place they want the notifications, and pastes its address under Settings, Connectors.

- Google Chat: in a space, open Apps and integrations, then Webhooks, and make one. The address looks like `https://chat.googleapis.com/v1/spaces/.../messages?key=...`.
- Microsoft Teams: in the Workflows app, make a workflow that posts to a chat or channel when a webhook request is received, and copy its address.

Hearth then posts the same line the person's inbox shows, with a link to open the item. Quiet hours hold it, as they hold email and push. A notification is never posted to somebody else's webhook.

## What Hearth checks

The address is a credential, since whoever holds it can post. Hearth stores it encrypted, shows it to nobody, and only calls the vendors' own hosts: `chat.googleapis.com` for Google, and Microsoft's `webhook.office.com`, `logic.azure.com` and `powerplatform.com` for Teams. An address anywhere else is refused, so a pasted address cannot point Hearth at a machine inside your network.

If a post fails, the Connectors page shows the reason without the address. Stopping the copy deletes the address.
