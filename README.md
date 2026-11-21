# 🔥 Hearth

### The workspace you own. Not rent. Not "license." Not "subscribe to." **Own.**

Email, messages, docs, tasks, canvas, calls, CRM, time off, daily reports, and agents. One Go binary, Postgres,
OpenSearch, and Garage for storage. That is the whole deployment. It runs on your box, in your building, on your
electricity bill, and answers to exactly one landlord: **you.**

**It is free. All of it. Including the parts everyone else charges you for.**

Single sign-on is not a premium feature here. Neither is the audit log. There is no Business plan, no Enterprise plan,
no Business Plus Pro Max Ultra plan, and no "contact sales," because there is only one tier and you are already on it.
Congratulations. You're Enterprise now. You're welcome.

> *"I looked at what my company was paying to rent its own to-do list back from a SaaS vendor, and I said 'absolutely
> not' out loud, in a meeting, to people who did not ask. Then I went home and wrote this. In Go. With a White
> Monster. A big one."*

⚠️ **DISCLAIMER**: No SaaS vendors were harmed in the making of this README. (Several were roasted. Lightly. On an open
flame. Until crispy.)

---

## 💀 Why This Exists (A Roast, Served Hot)

We are burning these bridges so we can light our way at night. Pull up a chair. It's warm by the hearth.

### 🟣 ClickUp: *"The Everything App"*

ClickUp calls itself the everything app, and honestly? Fair. Everything is in there. Your tasks. Your docs. Your
goals. Your whiteboards. Your will to live. A notification every time a coworker *breathes* near a task. It's all in
there. You will never find any of it again, but it is *in there*. It's a junk drawer with a sales team.

- ClickUp has more views than you have tasks. List. Board. Box. Gantt. Calendar. Timeline. Workload. Table. Mind Map.
  I opened the view picker in 2024 and I'm still scrolling. Tell my kids I love them.
- Every update moves the button you use most. I'm convinced it's a cognitive fitness program. My hippocampus has abs now.
- The sidebar has a sidebar. The sidebar's sidebar has settings. The settings have a Space. The Space has a Folder.
  The Folder has a List. The List has a task. The task has subtasks. The subtasks have a checklist. The checklist has
  my sanity, nested four levels deep, set to "Private."
- We left in August 2026. Getting all of our own stuff back out took two trips, which is why `hearthctl clickup-gap`
  exists. That's right: I wrote a dedicated tool for going back to my ex's place for my hoodie. They'd rearranged the
  whole apartment. The hoodie was in a Folder. In a Space. Behind a view.

### 🌈 monday.com: *Named after the worst day of the week, and it still undersold itself*

monday.com looks like a Fisher-Price toy that learned to invoice you. So many colors. So many little status pills.
"Working on it" in orange. "Stuck" in red. "Done" in green. My actual status is none of those. My status is "White
Monster #4, 2:47 AM, staring." They don't have a pill for that. It'd be on the Enterprise plan anyway.

- **Seat buckets.** You can't buy the number of seats you have. You buy 3, or 5, or 10, or 15. It's a bar that only
  sells pitchers. You have 11 people? Congratulations, you're paying for 15. Four ghosts on payroll. They don't do any
  work but they cost the same as Dave, so honestly, at least they're consistent with Dave.
- Automations come with a monthly allowance, like you're twelve and you get five bucks for mowing the lawn. Run out of
  automations? The robots go on strike until the first of the month. Even the bots have a union and *you* don't.
- The feature you actually need always lives one plan up. It will live one plan up until the heat death of the universe.
  You could upgrade to God Tier and the button you want would be greyed out with a tooltip that says *"Available on
  God Tier+."*

### 🧟 And the rest of the support group

- **Asana**: sounds like a yoga pose and costs like a yoga studio. Neither one fixed my back.
- **Jira**: not project management. It's a hostage situation with a burndown chart. The ticket has been "In Review"
  since the Obama administration. Nobody remembers who the reviewer is. The reviewer is a workflow transition now.
  The workflow has eleven states and none of them are "done," just like my therapy.
- **Notion**: a beautiful, empty, minimalist room that you pay rent on. Gorgeous. Serene. Everyone made a "Team Wiki"
  in it once. Nobody has opened it since. It's a gym membership for documentation.
- **HubSpot**: the "Starter" tier is a starter the way a free casino drink is free. We imported out of it with
  `hearthctl hubspot-import`. Delta import. Dry run by default. Walked out with every company, contact, deal, note and
  call, and I didn't even have to talk to an account manager named Chad about "growing together."
- **Slack**: the free plan hides everything older than 90 days, which is the only feature in tech that has ever
  improved my mental health. Respect. Still not paying for it.

### 📞 "Contact Sales"

The two most expensive words in the English language. "Contact Sales" is a button that means *"we are going to look at
your LinkedIn, your headcount and your Series letter, and then decide what you can afford."* It's not a price. It's a
vibe check with a quota.

I don't have a sales team. I have a `go build`.

### 🪪 The SSO Tax

You want your people to log in *securely*? With the identity provider you *already pay for*? That's the Enterprise
plan, sir. Security is a luxury good now. Like a Rolex. For logging in.

You want an **audit log**, so you can know who deleted the Q3 folder? Enterprise. You want to know who *did* things to
*your* data, in *your* company? Believe it or not, Enterprise.

Here, it's on. It's always been on. It's on in the free tier, which is the only tier, which is free. If you have to pay
extra for a seatbelt, it's not a car. It's a ransom note with wheels.

---

## 🧾 Whose Data Is It? (Mine. The Answer Is Mine.)

Here's the deal and I'm only going to say it once, loudly, at 3 AM:

**It's my data. If anybody is going to sell it, it's going to be me.**

Not you. Not your "trusted partners." Not your "we may use aggregated, de-identified insights to improve our
services." Not some vendor's Terms of Service that changes every spring in an email titled *"An update to our
policies"* that nobody has ever opened in the history of email.

To be clear, I'm not saying they sell it. I'm saying **I shouldn't have to wonder.** I'm saying the decision shouldn't
live in somebody else's boardroom, on somebody else's server, in somebody else's region, under somebody else's next
funding round. If I ever want to sell my own data, I'll sell it. To who I want. For White Monster money. And I'll keep
the receipt in my own Postgres, backed up by my own `hearthctl backup`, verified by my own `hearthctl verify`, sitting
in my own Garage.

Your company's work is not a "workspace" you're renting by the seat. It's your company. Own it.

---

## 🪦 Them vs. Hearth

| | The Rent-Your-Own-Work Industrial Complex | Hearth |
|---|---|---|
| Single sign-on | "Enterprise" (vibes-based pricing) | On. Free. Obviously. |
| Audit log | "Enterprise" (see above, cry) | On. Free. Every assistant call too. |
| Seats | Buckets. Pitchers. Ghosts. | However many humans you actually have |
| Where your data lives | Their cloud, their region, their mood | Your box. Your disk. Your Garage. |
| Price-increase email | Annual tradition, like a colonoscopy | Impossible. AGPLv3. There's no lever to pull. |
| "Smart" features | Add-on. Per seat. Per month. To summarize tasks *you wrote* | Bring your own assistant through the connector |
| "Contact Sales" | Yes, and they'll follow up. Forever. | I am the sales. I'm asleep. Or not. |
| Getting your data out | Export CSV and pray | It never left |
| Deployment | Not your problem (that's the problem) | One binary. Fight me. |

---

## ✅ Status

**2.22.12. In production. Carrying a real company's work. Every day.**

A deployment behind single sign-on serving tasks, work orders, documents, messages, mail, contacts, companies, deals, a
canvas, time off, time logging and daily reports, with an importer that moved the work off ClickUp and left the tree in
the shape the company *actually* has, not the shape a sidebar decided it should have. 476 Go files (tests included,
because I'm not a coward) over 65 migrations, against Postgres, OpenSearch and object storage.

`hearthctl` takes a backup, verifies the archive, puts it back, and rehearses the whole thing against a spare database,
so the recovery window is a number somebody *measured this quarter* rather than a paragraph somebody's marketing team
wrote. Your vendor has a "99.99% uptime" badge. I have a restore drill. One of those is a JPEG.

The version says it is depended on. It does not say it is finished. This is still one person's project and it still has
the edges of one. Those edges are sharp. I machined them myself.

---

## 🔌 Connectors (It Plays Well With Others. It Just Doesn't Pay Them.)

Owning your data doesn't mean living in a cave. Hearth talks to the world. It just doesn't move in with it.

- **🤖 Assistant connector (MCP).** Hearth serves one at `https://hearth.example.com/mcp` over streamable HTTP. Add it as
  a custom connector in your assistant, sign in as *yourself* through your own identity provider (OAuth, PKCE, no
  client secret, no token to paste), and every tool runs with *exactly* your rights on the website. Not more. Not some
  god-mode service account. You. Every call writes a log line: who, which client, which tool, how it went. See
  [`docs/assistant-connector.md`](docs/assistant-connector.md).
- **🦾 Agent tokens.** `hearth_agent_...` tokens for unattended automation, with their own identity and grants, made on
  the Agents page. Their writes can be suggestions, because I've met robots, and I've argued with one for 45 minutes.
- **🧩 REST API.** Documents, CRM (companies, contacts, deals, notes, calls) and time off under `/api/v1`, with bearer
  tokens minted on your account page. See [`docs/crm-api.md`](docs/crm-api.md).
- **🚪 Importers (the exit doors other apps forgot to build):**
  - ClickUp: `hearthctl import-clickup`, then `clickup-gap` for the stragglers, then `verify-clickup` to prove it.
    Every row written prints an `UNDO` line. See [`docs/clickup-gap.md`](docs/clickup-gap.md).
  - HubSpot: delta import, dry run by default. [`docs/hubspot-import.md`](docs/hubspot-import.md)
  - Zoho CRM: contacts export straight in. [`docs/zoho-contacts-import.md`](docs/zoho-contacts-import.md)
- **🔁 Sync:**
  - **Mautic**, both directions, every ten minutes. Hearth is the CRM, Mautic sends the email.
    [`docs/mautic-sync.md`](docs/mautic-sync.md)
  - **Google Workspace groups** follow Hearth teams and departments, not the other way around.
    [`docs/groups-sync.md`](docs/groups-sync.md)
  - **SCIM** for provisioning from your identity provider.
  - **EmberOps** (on Frappe), so accounting can invoice a real customer.
    [`docs/crm-sync.md`](docs/crm-sync.md), [`docs/emberops-backend.md`](docs/emberops-backend.md)
- **📬 Microsoft 365 and Google Workspace, read AND write.** Sign in with your tenant or your domain, then work in your
  own mailbox (the whole thing: open, send, reply, forward, archive, label), your calendar (both ways, with Meet and
  Teams links), your contacts (both ways, deletes included), and your files in OneDrive, SharePoint and Drive (upload,
  replace, rename, move, delete) without leaving Hearth. Every switch is off until an admin flips it, every token is
  sealed, and every call is made as *you*, so nobody reads a colleague's mailbox through somebody else's login. It's a
  company tool, not a surveillance tool. [`docs/microsoft-365.md`](docs/microsoft-365.md),
  [`docs/google-workspace.md`](docs/google-workspace.md)
- **📱 Push notifications** via plain Web Push. Hearth is an installable app. No extra service, no third-party push
  vendor reading your notifications. [`docs/push-notifications.md`](docs/push-notifications.md)

---

## 🪣 Storage: Garage. Not a Hyperscaler. Garage.

Your files go in **[Garage](https://garagehq.deuxfleurs.fr/)**, the S3-compatible object store that runs on your own
hardware, bundled with Hearth and on by default. Not a bucket in somebody's region. Not a bill that scales with your
anxiety. A store you run, on disks you can touch, that you can kick if it misbehaves.

- The Helm chart ships the `embernet-garage` chart, vendored, pinned by digest, so an upgrade needs no repo access. The
  app and the backup job read the **same** store, chosen by one setting (`s3.backend`), so they can never point at two
  different places and gaslight you during a restore.
- The Quadlet install runs Garage as its own rootless systemd service right next to Hearth. No root. No Docker.
- Hearth gets read and write on its one bucket. Not owner. It uploads and deletes objects and never manages buckets,
  because least privilege isn't an Enterprise feature either.
- Still on MinIO from an older install? It's supported for migration only: copy across with `hearthctl blob-migrate`, which checks
  every object byte for byte and never deletes a thing, then flip `s3.backend`
  ([`docs/storage/migrating.md`](docs/storage/migrating.md)). MinIO pulled its own images off its public registries in 2025,
  so a pod rescheduled onto a fresh node could just... not start. That's why we don't build on things we don't control.
  **That's the whole point of this entire README.**
- Insist on a cloud bucket anyway? Any S3 API works in four settings (`endpoint`, `region`, `useSSL`, keys), and Azure Blob and
  Google Cloud Storage are built in too ([`docs/storage`](docs/storage/README.md)). It's your
  data. I'm not your dad. I just think you can do better.

---

## 🚀 Run It

### One Linux box (Podman + Quadlet)

```bash
cd deploy/quadlet
./install.sh https://hearth.example.com
```

Hearth, Postgres 17 and Garage, each a rootless systemd user service, all updating themselves from their registry.
Needs Podman 5+, systemd, curl and openssl. Run it again and it keeps every secret and volume it already made. It
doesn't rotate your passwords on a whim like some kind of SaaS.

### Kubernetes (Helm)

```bash
helm upgrade --install hearth deploy/charts/hearth -n hearth --create-namespace
```

Postgres, OpenSearch and Garage come along for the ride. Read the header of
[`values.yaml`](deploy/charts/hearth/values.yaml) first for the Secret it expects (Postgres, S3 and OIDC credentials).
The chart doesn't generate its own credentials, because a chart that does either rotates them on every apply and kicks
everyone out, or needs a guard that silently does nothing. I've been that guy. Never again.

### From source

```bash
go build -o hearth ./cmd/hearth
HEARTH_PUBLIC_BASE_URL=https://hearth.example.com ./hearth
```

Go 1.26 or newer. The server is pure Go.

The interface is Astro and Svelte in `ui/`, and yes, building it means `npm ci`. **Once.** At build time. Then it gets
compiled to static files, embedded in the binary, and `node_modules` goes back in its cage where it belongs. It does
not ship. It does not run in production. It is not invited to the party. See [`ui/README.md`](ui/README.md).

---

## 📝 Daily Reports

Everyone files one report a day from **Daily reports** in the rail, or from the card on the dashboard: the day's text,
photos (the camera input opens the back camera on a phone), other files, and a comment, in one step. A person has at
most one report per day, in the office's time zone (America/New_York), enforced by a partial unique index (migration
00044). Deleting a report frees the day.

**Who can file.** Filing needs a department to file under. Migration 00044 adds `spaces.open_to_all` and creates one
"Daily reports" department per organisation with it set. A space with that flag counts as a department for every
account in the organisation, now and in future, with no per-person rows, so nobody is locked out of filing. It carries
the usual department meaning (`core.MemberSpaces`): you can open it and file into it, and you see only what is granted
to you, which is your own reports. Organisations that did not exist at migration time get the department made on first
use (`docs.EnsureReportSpace`). To change who reads a report, grant on the department a report is filed under.

**Reading.** The roll-up at `/reports/daily` filters by department (with everything beneath it), person and date range,
and shows only what the reader can already open. `/reports/daily.pdf` and `/documents/{id}/report.pdf` download the
roll-up or one report as a PDF with photos and comments, to save and email. There is no public link; nothing leaves
Hearth unless a person sends the file. *(Wild concept, I know. Data leaving only when a human decides it should.)*

---

## 🏎️ Why Rewrite It

The predecessor is a Rust fork of [`macro-inc/macro`](https://github.com/macro-inc/macro), archived read-only at
[`Fireball-Archive/hearth-rs`](https://github.com/Fireball-Archive/hearth-rs). It works, it runs on k3s, and it is
610,000 lines of Rust plus 572,000 lines of TypeScript across 27 pods wanting 24 to 32 GB. This project replaces it with
something one person can actually own. Owning things is the theme. Keep up.

| | Predecessor | Here |
|---|---|---|
| Image build | 1h32m | seconds |
| CI check | 36m8s | under a minute |
| Image size | 809 MB | 8.6 MB |
| Deployment | 27 pods, 6 datastores | 1 binary, Postgres, OpenSearch, Garage |
| Memory | 24 to 32 GB | target under 8 GB |

Real systems need real binaries, not 400 MB of `node_modules` and regret.

See [`PROVENANCE.md`](PROVENANCE.md) for the relationship between the two, which matters legally as well as
practically: this is an independent implementation, not a port.

---

## ⚖️ Licence

**AGPLv3, by deliberate choice rather than by inheritance.**

An independent implementation is not a derivative work, so nothing compelled this. It was picked with a free hand,
which makes it a harder promise rather than a softer one: there is no mechanism by which anyone maintaining this can put
a security control behind a paid tier later. Not me. Not a future me who got a sales team and a fleece vest. Nobody.

Section 13 is the load-bearing part. If you interact with this over a network, you are entitled to its source. Not a
tarball in six months, not a "community edition" with the useful parts removed, the actual source of the thing you are
talking to. That binds the maintainers exactly as hard as it binds anyone else who forks it.

I put the handcuffs on myself so I could never become the thing this README is about.

---

## 🛠️ Where Development Happens

Here. Issues and pull requests go to this repository, and releases are cut from it: a version tag builds the container
image and publishes the Helm chart. The earlier private repository, `hearth-original`, holds the full history up to the
public release and is kept as an archive.

---

**TL;DR**: Your company's work is yours. Stop renting it back by the seat. Stop paying a "security tax" to log in. Stop
buying pitchers of seats for ghosts. Run one binary, put your files in your own Garage, plug in whatever connectors you
want, and if anybody ever sells your data, make sure it's you.

*Last updated: 3 AM, obviously. Between a White Monster and a release tag.*

*P.S. If you work at one of the companies above and you read this whole thing: no hard feelings. Actually, some hard
feelings. Come self-host with us. It's warm by the fire. Say sorry to your mom.*

---

Built by [Fireball Industries](https://fireballz.ai). Operated on [EmberNet](https://embernet.ai).

&copy; 2026 Fireball Industries
