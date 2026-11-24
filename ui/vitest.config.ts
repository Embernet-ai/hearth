import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// The views are drawn on the server side in these tests (svelte/server) into a DOM, and the assertions read the DOM: what a
// person with a given set of permissions is shown, and what they are not. The pages keep the whitespace between elements,
// as the build does.
export default defineConfig({
  plugins: [svelte({ onwarn: () => {}, dynamicCompileOptions: ({ filename }) => ({ preserveWhitespace: /[\\/]views[\\/]pages[\\/]/.test(filename) }) })],
  resolve: { conditions: ['node'] },
  // No on-disk module cache. views/index.ts finds the pages with import.meta.glob, and the cache is keyed on that file's text,
  // not on the folder it lists, so a checkout that had run the tests before a page was added kept answering "no view for this
  // page" for it, from a cache that nothing would ever invalidate.
  test: { environment: 'node', include: ['src/**/*.spec.ts'], fsModuleCache: false },
});
