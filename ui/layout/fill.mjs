// Dead-space gate for the screens that fill the width of the window: the dashboard, inbox, outbox, settings, unassigned, channels and contacts.
// Draws each of their page fixtures with the real views and the real stylesheets (the shell, the side panel and every ancestor the app has) in a
// browser at 1280, 1440, 1920 and 2560 pixels wide, and at 390 for a phone, and fails on
//   - content that stops short of the window's edge: more than GUTTER px unused at the left or the right of the workspace beside the side panel,
//   - a card, panel or tile wider than 20rem whose text and controls span under 35% of it (a wide box with a short line in it),
//   - a page that scrolls sideways, or boxes that overlap, which check.mjs already forbids.
// It prints the numbers for every case (--table), which is how the before and after in the release notes were taken.
// usage: npm run build && npm run build:ssr && node layout/fill.mjs [--table] [--shots DIR]
import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { chromium } from 'playwright';

const root = path.resolve(import.meta.dirname, '..');
const args = process.argv.slice(2);
const opt = (k) => (args.includes(k) ? args[args.indexOf(k) + 1] : null);
const shotsDir = opt('--shots');
const table = args.includes('--table');
const GUTTER = 48; // px: the page's own padding is at most 2rem, plus a little for a scrollbar
const MIN_FILL = 0.35;

const css = [fs.readFileSync(path.join(root, '..', 'internal/web/static/hearth.css'), 'utf8')];
for (const f of fs.readdirSync(path.join(root, 'dist/_astro')).filter((f) => f.endsWith('.css')).sort()) css.push(fs.readFileSync(path.join(root, 'dist/_astro', f), 'utf8'));
const style = css.join('\n').replace(/url\(\/_astro\/[^)]*\)/g, 'url()');

const SCREENS = new Set(['dashboard', 'inbox', 'outbox', 'settings', 'unassigned', 'channels', 'contacts']);
const base = {
  Title: '', Path: '/', Version: '', Message: '', Today: '2026-11-21',
  Identity: { PrincipalID: 'me', OrgID: 'o', DisplayName: 'Me', Subjects: [], MustChangePassword: false },
  Build: { Revision: 'test', Profile: 'test' }, Can: {}, IsAdmin: false, SalesNav: true,
};
const cases = [];
const dir = path.join(root, 'src/views/fixtures');
for (const f of fs.readdirSync(dir).filter((f) => f.endsWith('.json')).sort()) {
  const name = f.replace(/\.json$/, '');
  const template = name.split('-')[0];
  if (!SCREENS.has(template)) continue;
  const data = JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8'));
  const page = { ...base, ...data, Identity: data.Identity ?? base.Identity, Build: data.Build?.Revision ? data.Build : base.Build, Can: data.Can ?? base.Can };
  cases.push({ name, env: { template, page, assets: { css: [], scripts: [] } } });
}

function draw(env) {
  const r = spawnSync('node', [path.join(root, 'dist-ssr/render.mjs')], { input: JSON.stringify(env), maxBuffer: 1 << 28 });
  if (r.status !== 0) throw new Error('render failed: ' + r.stderr.toString().slice(0, 400));
  return r.stdout.toString();
}

// Runs in the page.
function measure({ GUTTER, MIN_FILL }) {
  const vis = (el) => { const s = getComputedStyle(el); const r = el.getBoundingClientRect(); return s.display !== 'none' && s.visibility !== 'hidden' && r.width > 0 && r.height > 0; };
  const rem = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
  const vw = document.documentElement.clientWidth;
  const main = document.querySelector('main');
  if (!main) return { problems: ['no main element drawn'] };
  const rail = document.querySelector('aside.sidenav');
  const rr = rail && vis(rail) ? rail.getBoundingClientRect() : null;
  const left0 = rr && rr.right > 1 ? rr.right : 0;
  const problems = [];
  let minL = Infinity, maxR = -Infinity;
  for (const el of main.querySelectorAll('*')) {
    if (!vis(el) || ['SCRIPT', 'STYLE', 'OPTION', 'BR'].includes(el.tagName)) continue;
    const r = el.getBoundingClientRect();
    minL = Math.min(minL, r.left); maxR = Math.max(maxR, Math.min(r.right, vw));
  }
  const deadLeft = Math.max(0, Math.round(minL - left0)), deadRight = Math.max(0, Math.round(vw - maxR));
  if (deadLeft > GUTTER) problems.push(`dead space at the left: ${deadLeft}px unused beside the side panel`);
  if (deadRight > GUTTER) problems.push(`dead space at the right: ${deadRight}px unused at the edge of the window`);
  if (document.documentElement.scrollWidth > vw + 1) problems.push(`page scrolls sideways: ${document.documentElement.scrollWidth}px in ${vw}px`);

  // a wide box with a short line in it: any element that draws a box (a border or a fill), wider than 20rem, whose text and controls span
  // under MIN_FILL of its width. A link that is itself a block (a whole settings row) counts across its full box: the whole row is the target.
  const label = (el) => el.tagName.toLowerCase() + (typeof el.className === 'string' && el.className.trim() ? '.' + el.className.trim().split(/\s+/).join('.') : '') + ' "' + (el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 40) + '"';
  const pageBg = getComputedStyle(document.body).backgroundColor;
  const boxy = (el) => {
    const s = getComputedStyle(el);
    // a box has its border on at least three sides; one hairline (a divider between bands or rows) is not a box
    const sides = ['Top', 'Right', 'Bottom', 'Left'].filter((k) => parseFloat(s['border' + k + 'Width']) > 0 && s['border' + k + 'Style'] !== 'none' && !/rgba\(0, 0, 0, 0\)|transparent/.test(s['border' + k + 'Color']));
    const border = sides.length >= 3;
    const bg = s.backgroundColor; const fill = bg && !/rgba\(0, 0, 0, 0\)|transparent/.test(bg) && bg !== pageBg;
    return border || fill;
  };
  let worst = 1;
  for (const card of main.querySelectorAll('*')) {
    if (['INPUT', 'SELECT', 'TEXTAREA', 'BUTTON', 'A', 'SPAN', 'LABEL', 'SUMMARY', 'TABLE', 'THEAD', 'TH', 'TD', 'IMG', 'SVG', 'CODE'].includes(card.tagName)) continue;
    if (card.tagName === 'TR' && getComputedStyle(card).display !== 'grid') continue; // a table row is as wide as its table, and a phone's stacked row is the app's own card
    if (!vis(card) || !boxy(card)) continue;
    const box = card.getBoundingClientRect();
    if (box.width < 20 * rem || box.height < 2 * rem) continue;
    let l = Infinity, r2 = -Infinity, n = 0;
    const take = (rc) => { if (rc.width < 1 || rc.height < 1) return; n++; l = Math.min(l, rc.left); r2 = Math.max(r2, rc.right); };
    const walker = document.createTreeWalker(card, NodeFilter.SHOW_TEXT);
    for (let t = walker.nextNode(); t; t = walker.nextNode()) { if (!t.textContent.trim()) continue; const rg = document.createRange(); rg.selectNodeContents(t); for (const rc of rg.getClientRects()) take(rc); }
    for (const c of card.querySelectorAll('input,select,textarea,button,img,svg,canvas,progress,meter')) if (vis(c)) take(c.getBoundingClientRect());
    for (const a of card.querySelectorAll('a')) if (vis(a) && getComputedStyle(a).display !== 'inline') take(a.getBoundingClientRect());
    if (!n) continue;
    const fill = (r2 - l) / box.width;
    worst = Math.min(worst, fill);
    if (fill < MIN_FILL) problems.push(`wide box, short content: text and controls span ${Math.round(fill * 100)}% of the ${Math.round(box.width)}px of ${label(card)}`);
  }
  return { vw, left0, deadLeft, deadRight, deadPct: +(100 * (deadLeft + deadRight) / (vw - left0)).toFixed(1), worstFill: +(worst * 100).toFixed(0), problems };
}

const executablePath = process.env.CHROME || undefined;
const browser = await chromium.launch({ executablePath });
let bad = 0;
const rows = [];
if (shotsDir) fs.mkdirSync(shotsDir, { recursive: true });
for (const c of cases) {
  let body;
  try { body = draw(c.env); } catch (e) { console.log(`FAIL ${c.name}: ${e.message}`); bad++; continue; }
  const html = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><style>${style}</style></head><body>${body}</body></html>`;
  for (const [vw, vh] of [[1280, 800], [1440, 900], [1920, 1080], [2560, 1440], [390, 844]]) {
    const ctx = await browser.newContext({ viewport: { width: vw, height: vh }, colorScheme: 'light' });
    const page = await ctx.newPage();
    await page.setContent(html, { waitUntil: 'load' });
    const m = await page.evaluate(measure, { GUTTER, MIN_FILL });
    rows.push({ name: c.name, vw, ...m });
    if (shotsDir) await page.screenshot({ path: path.join(shotsDir, `${c.name}-${vw}.png`), fullPage: true });
    for (const p of m.problems) { console.log(`FAIL ${c.name} @${vw}: ${p}`); bad++; }
    await ctx.close();
  }
}
await browser.close();
if (table) { console.log('case\twidth\tdeadLeft\tdeadRight\tdead%\tworstFill%'); for (const r of rows) console.log([r.name, r.vw, r.deadLeft, r.deadRight, r.deadPct, r.worstFill].join('\t')); }
console.log(bad ? `${bad} dead-space problems in ${cases.length} pages` : `fill ok: ${cases.length} pages at 1280, 1440, 1920, 2560 and 390`);
process.exit(bad ? 1 : 0);
