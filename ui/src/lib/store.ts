// Small per-browser memory, always behind try/catch: storage may be blocked or absent, and the
// page has to read the same without it.
const NAV_KEY = 'hearth.nav';

function read(): Record<string, boolean> {
  try {
    return JSON.parse(localStorage.getItem(NAV_KEY) || '{}') || {};
  } catch {
    return {};
  }
}

/** Whether a rail section is open: the remembered choice, or `fallback`. */
export function navOpen(key: string, fallback: boolean): boolean {
  const v = read()[key];
  return typeof v === 'boolean' ? v : fallback;
}

export function setNavOpen(key: string, open: boolean) {
  try {
    const s = read();
    s[key] = open;
    localStorage.setItem(NAV_KEY, JSON.stringify(s));
  } catch {
    /* nothing to persist to */
  }
}

const THEME_KEY = 'hearth-theme';

export function currentTheme(): 'day' | 'dark' {
  if (typeof document === 'undefined') return 'dark'; // drawn on a server (the acceptance suite): there is no page yet
  return document.documentElement.getAttribute('data-theme') === 'day' ? 'day' : 'dark';
}

export function toggleTheme(): 'day' | 'dark' {
  const next = currentTheme() === 'day' ? 'dark' : 'day';
  document.documentElement.setAttribute('data-theme', next);
  try {
    localStorage.setItem(THEME_KEY, next);
  } catch {
    /* the toggle still works for this page */
  }
  return next;
}
