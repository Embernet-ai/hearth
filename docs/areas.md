# Marketing, Forms, Social and Tools

Four areas of Hearth work against apps you already run. Marketing, Forms and Social read and write the records in those apps with the
API of each, so what a person changes in Hearth is what the app holds, and what the app holds is what Hearth shows. Tools is a launcher
to the other apps your company uses.

All four are in the rail on every installation. An area whose app is not set up says so on its page and names the settings to
make, so nobody has to guess why it is empty. They need a signed-in person; contractors do not get them unless an administrator
lists the page.

## Marketing

Emails, campaigns and segments.

* Emails: list, search, create, edit, publish and unpublish, give a go-live time, send to the segments now, and delete.
* Campaigns: list, create with the email they send and how many days after joining, edit the dates, publish and unpublish, delete. The
  page shows how many people are in each.
* Segments: list with how many people are in each, create, edit and delete.

Set these on the server (the chart reads them from `marketing` in the values and from a Secret for the client):

| Setting | Meaning |
|---|---|
| `HEARTH_MARKETING_URL` | Address of the email marketing app. Use the address Hearth can reach, inside the cluster if it runs there. |
| `HEARTH_MARKETING_CLIENT_ID`, `HEARTH_MARKETING_CLIENT_SECRET` | An API client that uses the client credentials grant and may read and write emails, campaigns and segments. |

## Forms

Forms and their answers.

* List forms with their state: draft, scheduled, live, paused or closed.
* Create and edit a form from a plain list of questions, one a line: `[text]`, `[choice]`, `[multi]`, `[rating]` or `[nps]`, with choices after
  `::` separated by `;`, and a trailing `*` for a required question. For example `[choice] How did you hear about us? * :: A friend; Search`.
* Publish now, pause, close, or return to draft. Schedule the day a form goes live and the day it closes. The app keeps whole days, so
  a form goes live at the start of the day chosen.
* Read the answers, one column to a question, and delete a form with its answers.

| Setting | Meaning |
|---|---|
| `HEARTH_FORMS_URL` | Address of the forms app. |
| `HEARTH_FORMS_API_KEY` | An API key with read and write access to the forms. |

## Social

Channels, posts and their numbers.

* The connected channels, and each one's numbers for the last thirty days.
* Write a post for one or more channels and publish now, schedule it, or keep it as a draft.
* The posts of the last month and the next three, with their state, and delete any of them.

Connecting a channel (the sign-in with the social network) happens in the social app. Until one is connected, the page says so. The
app limits how often it can be asked, so Hearth keeps the channel list and posts for a short while and says so when the limit is hit.

| Setting | Meaning |
|---|---|
| `HEARTH_SOCIAL_URL` | Address of the social publishing app. |
| `HEARTH_SOCIAL_API_KEY` | Its API key. |

## Tools

A card for each app your company opens often, each linking to the live service and opening in a new tab. Set `HEARTH_TOOLS` to a JSON
list; in the chart it is the `tools` value:

```yaml
tools:
  - name: Ops
    description: Orders, invoices and stock.
    href: https://ops.example.com
  - name: Vord
    description: Your account and sign-in.
    href: https://vord.example.com
    note: Signs in with Vord.
```

A card needs a name and an `http` or `https` address, and the server refuses to start with a mistake in the list rather than show
a dead card. With no list, the Tools entry is not in the rail.
