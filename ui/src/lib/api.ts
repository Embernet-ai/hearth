import type { Envelope } from './types';

// Every address the server has is a page of the interface, so an address in the bar and the address the
// data is fetched from are the same string. These are the exceptions: downloads, images, feeds and the
// requests a machine makes. A link to one is left to the browser.
const NOT_PAGES = /^\/(files|avatar|export|calendar\/feed|inbound|static|api|events|login\/sso|auth\/|integrations\/[a-z0-9]+\/connect|logout$|_astro|theme-next\.js|sw\.js|healthz|readyz|manifest)|\.(csv|ics|pdf|png|jpg|svg|zip|xlsx|js|css|webmanifest)(\?|$)|\/(avatar|qr|label|download|export)(\?|$|\/)/;

/** Whether an address (path and query, same origin) is a page the interface draws. */
export function isPage(address: string): boolean {
  const path = address.split('?')[0];
  return !NOT_PAGES.test(address) && !isFile(address) && !/\.[a-z0-9]{2,4}$/i.test(path);
}

/**
 * Whether an address asks for a file: a report or list in csv or xlsx. The format is in the query, not on the path, so the
 * extension check above cannot see it. Drawn as a page it was fetched as JSON, answered with a file and thrown away: every
 * Download button on the time report did nothing. A link or a form for one is left to the browser, which saves the file or,
 * when the sign-in has ended, is sent to sign in and back.
 */
export function isFile(address: string): boolean {
  const q = address.split('?')[1] ?? '';
  return /(^|&)format=(csv|xlsx)(&|#|$)/i.test(q.split('#')[0]);
}

/** The same address with the file format taken off: the report page a file came from. */
export function withoutFormat(address: string): string {
  const [path, rest = ''] = address.split('?');
  const q = rest.split('&').filter((kv) => !/^format=/i.test(kv)).join('&');
  return q ? `${path}?${q}` : path;
}

export class PageError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

/**
 * One page's data. The server decides what this person may see exactly as it does
 * for the page: a signed-out request is redirected by the server to the
 * sign-in page, and that arrives here as the login page's data.
 */
export async function getPage(path: string, signal?: AbortSignal): Promise<{ env: Envelope; url: string; status: number }> {
  const res = await fetch(path, { headers: { Accept: 'application/json' }, credentials: 'same-origin', signal });
  const ct = res.headers.get('Content-Type') ?? '';
  if (!ct.includes('application/json')) {
    throw new PageError(res.status, `The server answered ${res.status} with something that is not page data.`);
  }
  return { env: (await res.json()) as Envelope, url: new URL(res.url).pathname + new URL(res.url).search, status: res.status };
}

/**
 * A form post, to the same address with the same fields the form carries.
 * The server answers with a redirect to where the action leads; fetch follows it
 * with the JSON Accept header, so the answer is the next page's data.
 */
/** A form's fields as the form carries them: urlencoded, unless it carries a file (then multipart). */
export function encode(data: FormData): FormData | URLSearchParams {
  for (const [, v] of data.entries()) if (v instanceof File) return data;
  const p = new URLSearchParams();
  for (const [k, v] of data.entries()) p.append(k, v as string);
  return p;
}

export async function post(action: string, body: FormData | URLSearchParams): Promise<{ env: Envelope; url: string; status: number }> {
  const res = await fetch(action, { method: 'POST', body: body instanceof FormData ? encode(body) : body, headers: { Accept: 'application/json' }, credentials: 'same-origin' });
  const ct = res.headers.get('Content-Type') ?? '';
  if (!ct.includes('application/json')) {
    // A sign-in that sends the person back to a file they asked for (a report download whose session had ended): the browser
    // saves the file, and the interface shows the report it came from.
    const to = new URL(res.url).pathname + new URL(res.url).search;
    if (res.redirected && isFile(to)) {
      location.assign(to);
      return getPage(withoutFormat(to));
    }
    throw new PageError(res.status, `The server answered ${res.status}.`);
  }
  return { env: (await res.json()) as Envelope, url: new URL(res.url).pathname + new URL(res.url).search, status: res.status };
}
