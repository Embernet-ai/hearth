import { isPage } from './api';

// A tiny router: the address bar holds the server's own address, and a click on a link inside the
// app is turned into a pushState so the page is not reloaded.
class Router {
  /** The server path and query being shown. */
  current = $state(location.pathname + location.search);

  constructor() {
    addEventListener('popstate', () => {
      this.current = location.pathname + location.search;
    });
  }

  go(address: string, replace = false) {
    const u = new URL(address, location.origin);
    const url = u.pathname + u.search;
    if (replace) history.replaceState(null, '', url);
    else history.pushState(null, '', url);
    this.current = location.pathname + location.search;
  }
}

export const router = new Router();

/** Click handler for links: plain left clicks on pages stay in the app. */
export function intercept(e: MouseEvent) {
  const a = (e.target as HTMLElement).closest('a');
  if (!a || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  if ((a.target && a.target !== '_self') || a.hasAttribute('download')) return;
  const u = new URL(a.href, location.origin);
  if (u.origin !== location.origin || !isPage(u.pathname + u.search) || (u.pathname === location.pathname && u.search === location.search && u.hash)) return;
  e.preventDefault();
  router.go(u.pathname + u.search);
}
