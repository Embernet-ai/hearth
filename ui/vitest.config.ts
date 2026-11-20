import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// The views are drawn on the server side in these tests (svelte/server) into a DOM, and the assertions read the DOM: what a
// person with a given set of permissions is shown, and what they are not. The pages keep the whitespace between elements,
// as the build does.
export default defineConfig({
  plugins: [svelte({ onwarn: () => {}, dynamicCompileOptions: ({ filename }) => ({ preserveWhitespace: /[\\/]views[\\/]pages[\\/]/.test(filename) }) })],
  resolve: { conditions: ['node'] },
  test: { environment: 'node', include: ['src/**/*.spec.ts'] },
});
