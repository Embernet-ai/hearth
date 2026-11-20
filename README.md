# Hearth

An all-in-one workspace you host yourself. Email, messages, docs, tasks, canvas, calls, CRM, and
agents. One Go binary, Postgres, OpenSearch, and object storage. That is the whole deployment.

**It is free. All of it. Including the parts everyone else charges you for.**

Single sign-on is not a premium feature here. Neither is the audit log. There is no Business plan,
no Enterprise plan, and no "contact sales," because there is only one tier and you are already on
it.

---

## Status

**1.0.0. In production, carrying a company's work.**

Concretely: a deployment behind single sign-on serving tasks, work orders, documents, messages,
mail, contacts, companies and a canvas, with an importer that moved the work off ClickUp and left
the tree in the shape the company actually has. 157 Go files over 25 migrations, 165 routes and 40
templates, against Postgres, OpenSearch and object storage. `hearthctl` takes a backup, verifies
the archive, puts it back, and rehearses the whole thing against a spare database, so the recovery
window is a number somebody measured this quarter rather than a paragraph somebody wrote.

The version says it is depended on. It does not say it is finished. This is still one person's
project and it still has the edges of one.

The predecessor is a Rust fork of [`macro-inc/macro`](https://github.com/macro-inc/macro), archived
read-only at [`Fireball-Archive/hearth-rs`](https://github.com/Fireball-Archive/hearth-rs). It
works, it runs on k3s, and it is 610,000 lines of Rust plus 572,000 lines of TypeScript across 27
pods wanting 24 to 32 GB. This project replaces it with something one person can actually own.

See [`PROVENANCE.md`](PROVENANCE.md) for the relationship between the two, which matters legally as
well as practically: this is an independent implementation, not a port.

## Daily reports

Everyone files one report a day from **Daily reports** in the rail, or from the
card on the dashboard: the day's text, photos (the camera input opens the back
camera on a phone), other files, and a comment, in one step. A person has at
most one report per day, in the office's time zone (America/New_York), enforced
by a partial unique index (migration 00044). Deleting a report frees the day.

**Who can file.** Filing needs a department to file under. Migration 00044 adds
`spaces.open_to_all` and creates one "Daily reports" department per
organisation with it set. A space with that flag counts as a department for
every account in the organisation, now and in future, with no per-person rows,
so nobody is locked out of filing. It carries the usual department meaning
(`core.MemberSpaces`): you can open it and file into it, and you see only what
is granted to you, which is your own reports. Organisations that did not exist
at migration time get the department made on first use (`docs.EnsureReportSpace`).
To change who reads a report, grant on the department a report is filed under.

**Reading.** The roll-up at `/reports/daily` filters by department (with
everything beneath it), person and date range, and shows only what the reader
can already open. `/reports/daily.pdf` and `/documents/{id}/report.pdf` download
the roll-up or one report as a PDF with photos and comments, to save and email.
There is no public link; nothing leaves Hearth unless a person sends the file.

## Why rewrite it

| | Predecessor | Here |
|---|---|---|
| Image build | 1h32m | seconds |
| CI check | 36m8s | under a minute |
| Image size | 809 MB | 8.6 MB |
| Deployment | 27 pods, 6 datastores | 1 binary, Postgres, OpenSearch, object storage |
| Memory | 24 to 32 GB | target under 8 GB |

Real systems need real binaries, not 400 MB of `node_modules` and regret.

## Licence

**AGPLv3, by deliberate choice rather than by inheritance.**

An independent implementation is not a derivative work, so nothing compelled this. It was picked
with a free hand, which makes it a harder promise rather than a softer one: there is no mechanism by
which anyone maintaining this can put a security control behind a paid tier later.

Section 13 is the load-bearing part. If you interact with this over a network, you are entitled to
its source. Not a tarball in six months, not a "community edition" with the useful parts removed,
the actual source of the thing you are talking to. That binds the maintainers exactly as hard as it
binds anyone else who forks it.

## Build

```bash
go build -o hearth ./cmd/hearth
HEARTH_PUBLIC_BASE_URL=https://hearth.example.com ./hearth
```

Go 1.26 or newer. No npm, no bundler, no `node_modules`.

## Where development happens

Here. Issues and pull requests go to this repository, and releases are cut from it: a version tag builds the container image and publishes the Helm chart. The earlier private repository, `hearth-original`, holds the full history up to the public release and is kept as an archive.

---

Built by [Fireball Industries](https://fireballz.ai). Operated on [Embernet](https://embernet.ai).

&copy; 2026 Fireball Industries
