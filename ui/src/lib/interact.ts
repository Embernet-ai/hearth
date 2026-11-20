import type { Envelope } from './types';
import { router } from './router.svelte';
import { isPage, encode } from './api';

// Forms in the pages post to the address they name, with the fields the form carries. One listener turns every such
// post into a fetch whose answer (the server redirects to where the action leads, and fetch follows with the JSON
// Accept header) is the next page's data. Downloads, uploads to presigned addresses and anything else that is not a page
// are left to the browser.
//
// Three more things the old pages did with a library and a server-rendered fragment are done here:
//   a form inside an embedded view (the quick view, the new panel) posts, and then that view reloads itself
//   a form marked data-quiet posts and the page refreshes in place, so an autosaving field keeps the cursor
//   a textarea marked data-preview shows the Markdown preview next to it, as the server renders it

type Apply = (env: Envelope, url: string) => void;
let apply: Apply = () => {};
export function bindInteractions(fn: Apply) {
  apply = fn;
}

/** Ask the page (and any embedded view) to fetch its data again and update in place. */
export function refreshPage(now = false) {
  window.dispatchEvent(new CustomEvent('hearth:refresh', { detail: { now } }));
}

function submit(ev: SubmitEvent) {
  const form = ev.target as HTMLFormElement;
  if (ev.defaultPrevented || !form?.action) return;
  const u = new URL(form.action, location.href);
  if (u.origin !== location.origin) return;
  const address = u.pathname + u.search;
  if (!isPage(address) && u.pathname !== '/logout') return;
  const method = (form.method || 'get').toLowerCase();
  ev.preventDefault();
  const data = new FormData(form, ev.submitter as HTMLElement | null);
  if (method === 'get') {
    const q = new URLSearchParams(data as any).toString();
    router.go(u.pathname + (q ? '?' + q : ''));
    return;
  }
  const host = form.closest('[data-embedded]');
  fetch(address, { method: 'POST', body: encode(data), headers: { Accept: 'application/json' }, credentials: 'same-origin' })
    .then(async (res) => {
      if (host || form.hasAttribute('data-quiet')) {
        if (!res.ok && res.status !== 303) {
          const why = res.headers.get('X-Hearth-Reason');
          if (why) { alert(why); return; }
        }
        if (host) host.dispatchEvent(new CustomEvent('hearth:reload'));
        else refreshPage(true);
        // A status change that moved subtasks with it comes back as ?closed=N&moved=<status> on the page it redirects to.
        const params = res.redirected ? new URL(res.url).searchParams : null;
        const closed = Number(params?.get('closed'));
        const moved = params?.get('moved') ?? '';
        const word = `subtask${closed === 1 ? '' : 's'}`;
        const label: Record<string, string> = { todo: 'To do', in_progress: 'In progress', blocked: 'Blocked', cancelled: 'Cancelled' };
        if (closed > 0) (window as any).hearthNotice?.(!moved || moved === 'done' ? `Closed with ${closed} ${word}` : `Moved ${closed} ${word} to ${label[moved] ?? moved}`);
        return;
      }
      const ct = res.headers.get('Content-Type') ?? '';
      if (!ct.includes('application/json')) {
        // The service worker answers a post made without a signal with a page of its own: a redirect back to where the person was
        // (the post is kept and sent later), or a "that one needs a connection" page. A browser would follow the redirect or show
        // that page, so this does the same. An answer that is neither (a toggle, a drop) has nothing to show.
        if (res.redirected) { router.go(new URL(res.url).pathname + new URL(res.url).search); return; }
        const html = await res.text();
        if (ct.includes('text/html') && html.trim()) { document.open(); document.write(html); document.close(); return; }
        if (res.ok) return;
        throw new Error(`The server answered ${res.status}.`);
      }
      const env = (await res.json()) as Envelope;
      const final = new URL(res.url);
      // A post that the server redirected moves the person to where the action leads. One it answered where it was sent (a form
      // refused, with the page and the reason) leaves them on the page they were on: adopting the form's own address made it the
      // address in the bar, and a form's address is POST-only, so the next refresh asked for it with GET and got a 404.
      apply(env, res.redirected ? final.pathname + final.search : location.pathname + location.search);
    })
    .catch((err) => alert(String(err.message ?? err)));
}

// ---- the Markdown preview ---------------------------------------------------------------------------------------------
// A textarea with data-preview posts its text to /preview, 300 ms after the last keystroke, and the answer (the text as
// the server renders it, which is what everybody else will read) goes into the next .mdpreview after it. data-preview-cards
// asks for the linked records to come back as cards.

const timers = new WeakMap<Element, number>();

function nextPreviewBox(from: Element): Element | null {
  for (const box of document.querySelectorAll('.mdpreview')) {
    if (from.compareDocumentPosition(box) & Node.DOCUMENT_POSITION_FOLLOWING) return box;
  }
  return null;
}

function preview(area: HTMLTextAreaElement) {
  const box = nextPreviewBox(area);
  if (!box) return;
  const body = new URLSearchParams({ body: area.value });
  if (area.hasAttribute('data-preview-cards')) body.set('cards', '1');
  fetch('/preview', { method: 'POST', body, credentials: 'same-origin' })
    .then((r) => (r.ok ? r.text() : ''))
    .then((html) => { box.innerHTML = html; })
    .catch(() => {});
}

function typed(ev: Event) {
  const area = ev.target as HTMLTextAreaElement;
  if (!area?.matches?.('textarea[data-preview]')) return;
  window.clearTimeout(timers.get(area));
  timers.set(area, window.setTimeout(() => preview(area), ev.type === 'mdpreview' ? 0 : 300));
}

export function installInteractions() {
  document.addEventListener('submit', submit);
  document.addEventListener('input', typed);
  document.addEventListener('mdpreview', typed);
}
