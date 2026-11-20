// @ts-check
import { defineConfig } from 'astro/config';
import svelte from '@astrojs/svelte';

// The Hearth interface, served by the Go binary from the build embedded in it
// (internal/web/next/dist). Every address the server has is a page of it: the shell
// is the answer to a browser navigation and the page's data is the answer to the
// same address asked for as JSON. Same origin, so the session cookie covers it.
//
// Static output, one page with a client side router. Hearth sends
// `script-src 'self'` with no 'unsafe-inline', so nothing may be inlined: scripts
// and styles are always separate files, and the app is mounted from a bundled
// module rather than an Astro island (islands ship an inline bootstrap script).
export default defineConfig({
  base: '/',
  trailingSlash: 'ignore',
  output: 'static',
  compressHTML: true,
  // The pages under src/views/pages were ported from Go templates, where the whitespace between elements and between the
  // items of a range is part of the text a person reads ("read write", not "readwrite"). Svelte trims whitespace at block
  // edges by default; those pages keep it. The components keep Svelte's default.
  integrations: [svelte({ dynamicCompileOptions: ({ filename }) => ({ preserveWhitespace: /[\\/]views[\\/]pages[\\/]/.test(filename) }) })],
  build: { assets: '_astro', inlineStylesheets: 'never' },
  vite: { build: { assetsInlineLimit: 0 } },
  devToolbar: { enabled: false },
});
