# Provenance

Hearth is an **independent implementation** written in Go. It shares no source code with
[`macro-inc/macro`](https://github.com/macro-inc/macro) or with its Rust fork.

That Rust fork is archived, read-only, at
[`Fireball-Archive/hearth-rs`](https://github.com/Fireball-Archive/hearth-rs). It was used as a
**functional specification**. It was not used as a source of code.

## What was derived from the predecessor, and what was not

| Derived | Not derived |
|---|---|
| The feature inventory: what the product does | Any Rust, TypeScript, or SQL source |
| The domain vocabulary: the nouns and the states they move through | Type definitions, field orders, error enums, constant tables |
| Observed HTTP behaviour of the running system | Any transliteration of a function into Go |
| Observed database structure of the running system | The 250-file migration set, copied or adapted |
| Operational configuration for Kubernetes, Postgres, MinIO, OpenSearch, and Entra | Comments, of any kind |

**The observed behaviour of a running program is not protected expression.** That is why the
capture under `docs/spec/` is both the most accurate specification available and the safest one.

## Working rules

1. The only artifact that may leave a predecessor source file is a **prose note, in your own
   words**, under `docs/spec/`. Never a snippet, never a struct, never a field list transcribed in
   order. Implement from the note with the original closed.
2. Every file under `docs/spec/` carries a `Derived from:` header naming its origin and date.
3. Two areas are **deliberately never read**, because reading them buys nothing and can only
   contaminate: the CRDT packages (this project has no CRDT), and the SolidJS component tree (this
   project is server-rendered Go).

## Licence

Hearth is **AGPLv3 by deliberate choice, not by inheritance.**

An independent implementation is not a derivative work, so nothing compelled this licence. It was
picked with a free hand, which makes it a harder promise rather than a softer one: there is no
mechanism by which the people who maintain this can put single sign-on, audit logs, or any other
security control behind a paid tier later.

See [`LICENSE`](LICENSE) for the full text, taken verbatim from gnu.org.

Copyright &copy; 2026 Fireball Industries.

## Third-party code shipped in Hearth

| What | Where | Licence |
|---|---|---|
| htmx | `internal/web/static/htmx.min.js` | Zero-Clause BSD |
| The rich-text editor: Tiptap, ProseMirror, marked and their dependencies (35 packages) | `internal/web/static/editor.js`, built from `editor/` | MIT, each listed with its licence text in `internal/web/static/editor-licenses.txt` |

Tiptap also sells Pro extensions and a hosted service; neither is used. `editor/build.mjs` fails
the build if anything it bundles is not MIT.
