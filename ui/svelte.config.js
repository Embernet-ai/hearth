import { vitePreprocess } from '@astrojs/svelte';

// TypeScript inside .svelte files goes through Vite's preprocessor, the setup
// @astrojs/svelte documents.
export default {
  preprocess: vitePreprocess(),
  compilerOptions: {
    // Pages under src/views/gen are written by tools/gotpl.mjs from the classic Go templates and keep their markup exactly
    // (a label without a control, a link without an href, a click on a div). Accessibility lint on that markup is a to do
    // for the hand refinement of each page, not a reason to change what the classic page shows today.
    warningFilter: (w) => !(w.code.startsWith('a11y_') && /src\/views\/gen\//.test(w.filename ?? '')) && w.code !== 'css_unused_selector',
  },
};
