# The interface

Hearth's interface is an Astro and Svelte app in `ui/`, built to static files that are embedded in the one Go binary. It is the only
interface: the Go templates it replaced are gone. Nothing about the API, MCP, the CRM sync, the service worker, offline writes, the
calendar feed or inbound mail changes because of it.

## How it is served

* `internal/web/next/dist` is embedded with `go:embed` (`internal/web/nextui.go`). A checkout holds only a placeholder, so `go build`
  and the Go tests never need Node. The Containerfile builds `ui/` in a `node` stage and copies the result there before compiling.
* Every address the server has is a page of the app. A browser navigation to `/documents/abc` gets the app's one page (`serveShell`,
  from `Server.render`), and the app then asks that same address for its data with `Accept: application/json` (or `?_data=1`). So a
  bookmark, an email link, or a link another system holds lands where it always did, a reload never 404s, and a refused page keeps its
  real status code.
* `/_astro/...` and `/theme-next.js` are the build's files. Files under `_astro/` are immutable for a year, the page itself is always
  revalidated, and a missing `_astro/` file is a 404, never the page. `/next/...`, where the first release put the app, redirects (308)
  to the same address at the root.
* Same origin, so the session cookie covers it and the content security policy (`script-src 'self'`) is unchanged: nothing in the
  build is inline.

## One data path

Every page is rendered through `Server.render` with one `page` value, and a request for data gets that value as JSON
(`internal/web/nextjson.go`, `nexttree.go`). The handler, its permission checks and everything `render` resolves for the caller ran
first, so the app cannot show anything the server would not have sent. Forms post to the same address with the same fields and follow
the same redirect; the answer to a post is the next page's data.

The walk differs from `encoding/json` on purpose: Go field names (not tag names), `omitempty` ignored (a view sees Go's zero values),
and the zero-argument methods the views read are added (`.Timer.Elapsed`, `.Active`), from `internal/web/next/data-methods.txt`. Named
string types that carry methods (`authz.Capability`) go out as `{"$": value, "Label": ...}`. A field listed in
`internal/web/next/data-markdown.txt` is rendered by the Go Markdown renderer and sent beside the field as `Name_md`. A test fails when
`page` gains a field that is not listed in `internal/web/next/page-fields.txt`, so sending a new field is a decision about who may
see it.

## Views

`ui/src/views/pages/<template>.svelte` draws the page the server names `<template>` (`s.render(w, r, "documents", ...)`). They were
ported from the Go templates with the markup, class names, attributes and gates kept, so the behavior scripts and the stylesheet
work on them unchanged. Go's template functions they call are ported in `ui/src/lib/gofuncs.ts` and held to the answers Go gave by
`gofuncs.golden.json`. The shell (`components/`), the sign-in page and the numbers panel are written by hand.

The behavior scripts (`internal/web/static`: `ui.js`, `live.js`, the editor, scan, canvas, board, spaces, drag and drop) bind to `data-*`
attributes. After a page is drawn the app fires `hearth:load` so the ones that set themselves up on a page do it for the new markup.
What the pages used to do with a library and a server-rendered fragment is done in the app:

* the quick view of a task and the new panel are `components/Embedded.svelte`, another page of the app drawn in a popover; a form
  inside it posts and the view reloads
* a field that saves itself (`data-quiet`) posts and the page refreshes in place
* `live.js` tells the app when something changed and the page fetches its data again (`hearth:refresh`)
* the Markdown preview is a delegated listener in `lib/interact.ts` (`data-preview`)

## Tests

* `internal/web/fixtures_test.go` builds the page values that decide what each role is shown and writes them as the JSON the server
  sends to `ui/src/views/fixtures`; `go test ./internal/web -run TestViewFixtures -update-fixtures` regenerates them, and without
  the flag the test fails when they are out of date.
* `ui/src/views/*.spec.ts` draw a view from a fixture and assert what a person with those permissions sees and does not see.
* `cmd/acceptance` runs against a real server. It asks for page data and, with `-render ui/dist-ssr/render.mjs` (`npm run build:ssr`),
  checks the page as the interface draws it, so its markup checks are checks on the real views. CI does both.

## Look

`ui/src/styles/house.css`: the embernet-design tokens (day) and the EmberCORE console's dark set, loaded after `hearth.css`, which keeps
the layout of boards, calendars and popovers. It changes colour, type, radius and shadow only. Contrast was measured for both palettes.

## The numbers on the dashboard

`internal/web/statspanel.go` builds them. Every tile is a query over live records: hours from `time_entries` against the weekly
capacity somebody set (`PersonHours`), work through the same query compiler the document list uses, deals through the deals list's own
filters (`PersonStats`, `ListQuery`), and a win or a loss is dated by `deals.closed_at`, which a trigger sets when a deal enters Won,
Lost or No response and clears when it leaves. Each tile names its period and links to the records it counts, because the link's
filters are the count's filters (`acceptance` checks that the list behind a tile holds what the tile counted). Somebody sees their own;
somebody who holds everybody's time (`authz.TimeAll`, which a super admin has) can pick a colleague, and then still sees only the work and
deals their own access reaches.
