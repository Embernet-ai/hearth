import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// One file that draws a page's data as the browser would, for the acceptance suite: npm run build:ssr, then
// node dist-ssr/render.mjs < page.json. Everything is bundled into it, so it runs wherever node does.
export default defineConfig({
  plugins: [svelte({ onwarn: () => {}, dynamicCompileOptions: ({ filename }) => ({ preserveWhitespace: /[\\/]views[\\/]pages[\\/]/.test(filename) }) })],
  ssr: { noExternal: true },
  build: {
    ssr: 'src/ssr.ts',
    outDir: 'dist-ssr',
    emptyOutDir: true,
    copyPublicDir: false,
    target: 'node22',
    rollupOptions: { output: { entryFileNames: 'render.mjs', format: 'esm', inlineDynamicImports: true } },
  },
});
