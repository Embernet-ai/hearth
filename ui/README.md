# Hearth interface (ui)

Astro and Svelte, built to static files that the Go binary embeds (see ../docs/next-interface.md).

    npm ci
    npm run build      # type check, svelte-check with no warnings, astro build to dist/
    npm test           # the ported template functions against Go's answers, and the views against the server's page data
    npm run build:ssr  # the interface drawn on the server, for the acceptance suite: dist-ssr/render.mjs

To try it in the binary: copy `dist/*` into `../internal/web/next/dist/`, build `cmd/hearth`, and open it.
The design tokens in `src/styles/tokens.css` are a copy of `Embernet-ai/embernet-design` (commit in the file's first line) so a build
needs no credential.
