import type { Envelope } from './types';

// The stylesheet and behavior scripts, loaded once from the paths the server stamps with the build revision.
// The scripts bind by delegated listeners on data-* attributes, so any markup that carries those attributes gets the behavior.
let loaded: Promise<void> | null = null;

function script(src: string): Promise<void> {
  return new Promise((resolve) => {
    const s = document.createElement('script');
    s.src = src;
    s.async = false;
    s.onload = () => resolve();
    s.onerror = () => resolve(); // a missing optional script must not stop the page
    document.head.appendChild(s);
  });
}

export function loadAssets(a: Envelope['assets']): Promise<void> {
  if (loaded) return loaded;
  // The stylesheet goes first, so the shell's own rules (loaded with the page) win where they overlap.
  const first = document.querySelector('link[rel=stylesheet]');
  for (const href of a.css) {
    const l = document.createElement('link');
    l.rel = 'stylesheet';
    l.href = href;
    document.head.insertBefore(l, first);
  }
  loaded = (async () => {
    for (const src of a.scripts) await script(src);
  })();
  return loaded;
}

/**
 * Tell the behavior scripts that new markup has been drawn, so the ones that set themselves up on a page (the rich text
 * editor, the scan page, the canvas, the department tree) do it for what is there now. They listen for hearth:load and guard
 * against binding twice, so announcing a whole page is safe.
 */
export function announce(el: Element | null) {
  if (!el) return;
  el.dispatchEvent(new CustomEvent('hearth:load', { bubbles: true, detail: { elt: el } }));
}
