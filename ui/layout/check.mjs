// Layout check: draws pages with the real views and the real stylesheets in a browser, at desktop and phone width, and fails on
//   - sideways scrolling of the page,
//   - two boxes that overlap when neither contains the other,
//   - a card or tile that is mostly dead space: its content spans under half of its height (box taller than 6rem).
// Without arguments it draws every page fixture (src/views/fixtures). With --envelopes DIR it draws the page data in DIR instead
// (files saved from the server as the JSON the interface fetches, wrapped as {template, page}), which is how it is run against real data.
// usage: npm run build && npm run build:ssr && node layout/check.mjs [--envelopes DIR] [--shots DIR]
import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { chromium } from 'playwright';

const root = path.resolve(import.meta.dirname, '..');
const args = process.argv.slice(2);
const opt = (k) => (args.includes(k) ? args[args.indexOf(k) + 1] : null);
const shotsDir = opt('--shots');
const envDir = opt('--envelopes');

const css = [fs.readFileSync(path.join(root, '..', 'internal/web/static/hearth.css'), 'utf8')];
for (const f of fs.readdirSync(path.join(root, 'dist/_astro')).filter((f) => f.endsWith('.css')).sort()) css.push(fs.readFileSync(path.join(root, 'dist/_astro', f), 'utf8'));
// the build rewrites font addresses to /_astro/..., which a page set from a string cannot fetch: the fallback faces are fine for layout
// FONT_STRESS=1 draws every page with text a size larger and slightly wider than the real thing, so a layout that only just fits does not pass here and fail on a machine with other fonts
const stress = process.env.FONT_STRESS === '1' ? `\nhtml{font-size:${process.env.STRESS_SIZE || '112.5%'}} *{letter-spacing:.02em}` : '';
const style = css.join('\n').replace(/url\(\/_astro\/[^)]*\)/g, 'url()') + stress;

const base = {
  Title: '', Path: '/', Version: '', Message: '', Today: '2026-11-17',
  Identity: { PrincipalID: 'me', OrgID: 'o', DisplayName: 'Me', Subjects: [], MustChangePassword: false },
  Build: { Revision: 'test', Profile: 'test' }, Can: {}, IsAdmin: false, SalesNav: true,
};

const cases = [];
if (envDir) {
  for (const f of fs.readdirSync(envDir).filter((f) => f.endsWith('.json')).sort()) cases.push({ name: f.replace(/\.json$/, ''), env: JSON.parse(fs.readFileSync(path.join(envDir, f), 'utf8')) });
} else {
  const dir = path.join(root, 'src/views/fixtures');
  for (const f of fs.readdirSync(dir).filter((f) => f.endsWith('.json')).sort()) {
    const name = f.replace(/\.json$/, '');
    if (/^(setup|setpassword)/.test(name)) continue; // signed out pages, drawn by another component
    const template = name.split('-')[0];
    const data = JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8'));
    // a fixture says null for what the test did not set; the shell needs a signed-in person to draw the side panel
    const page = { ...base, ...data, Identity: data.Identity ?? base.Identity, Build: data.Build?.Revision ? data.Build : base.Build, Can: data.Can ?? base.Can };
    cases.push({ name, env: { template, page, assets: { css: [], scripts: [] } } });
  }
}

function draw(env) {
  const r = spawnSync('node', [path.join(root, 'dist-ssr/render.mjs')], { input: JSON.stringify(env), maxBuffer: 1 << 28 });
  if (r.status !== 0) throw new Error('render failed: ' + r.stderr.toString().slice(0, 400));
  return r.stdout.toString();
}

// Runs in the page. Returns a list of problems.
function inspect() {
  const problems = [];
  const vis = (el) => { const s = getComputedStyle(el); const r = el.getBoundingClientRect(); return s.display !== 'none' && s.visibility !== 'hidden' && r.width > 1 && r.height > 1; };
  const label = (el) => el.tagName.toLowerCase() + (el.id ? '#' + el.id : '') + (el.className && typeof el.className === 'string' ? '.' + el.className.trim().split(/\s+/).join('.') : '') + ' "' + (el.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 40) + '"';
  const main = document.querySelector('main');
  if (!main) return ['no main element drawn'];
  const scroller = (el) => { for (let e = el; e && e !== document.body; e = e.parentElement) { const o = getComputedStyle(e).overflowX; if (o === 'auto' || o === 'scroll') return true; } return false; };

  if (document.documentElement.scrollWidth > window.innerWidth + 1) problems.push(`page scrolls sideways: ${document.documentElement.scrollWidth}px in ${window.innerWidth}px`);

  // overlap: boxes that share a parent and cut into each other
  const parts = [...main.querySelectorAll('*')].filter((el) => vis(el) && !scroller(el) && getComputedStyle(el).position !== 'absolute' && getComputedStyle(el).position !== 'fixed' && !el.closest('details:not([open]) > :not(summary)') && !['BR', 'OPTION', 'TBODY', 'THEAD', 'TR', 'TD', 'TH', 'LI', 'SPAN', 'A', 'EM', 'STRONG', 'SMALL', 'CODE', 'INPUT'].includes(el.tagName) || el.tagName === 'INPUT' && vis(el));
  const byParent = new Map();
  for (const el of parts) { const p = el.parentElement; if (!byParent.has(p)) byParent.set(p, []); byParent.get(p).push(el); }
  for (const kids of byParent.values()) {
    for (let i = 0; i < kids.length; i++) for (let j = i + 1; j < kids.length; j++) {
      const a = kids[i].getBoundingClientRect(), b = kids[j].getBoundingClientRect();
      const w = Math.min(a.right, b.right) - Math.max(a.left, b.left), h = Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top);
      if (w > 2 && h > 2) problems.push(`overlap ${Math.round(w)}x${Math.round(h)}: ${label(kids[i])} / ${label(kids[j])}`);
    }
  }
  // text that spills out of its own box
  for (const el of main.querySelectorAll('button,.button,.chip,.tag,.stat,.card,.card2,th,td,label')) {
    if (!vis(el) || scroller(el)) continue;
    if (el.scrollWidth > el.clientWidth + 2 && getComputedStyle(el).overflowX === 'visible' && el.clientWidth > 0 && !['TD', 'TH'].includes(el.tagName)) problems.push(`content spills out of ${label(el)}`);
  }
  // dead space: how much of a card its content actually spans
  const rem = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
  for (const card of main.querySelectorAll('.card, .stat, .boardcol, .dailycard')) {
    if (!vis(card) || scroller(card)) continue;
    const box = card.getBoundingClientRect();
    let top = Infinity, bottom = -Infinity, left = Infinity, right = -Infinity, n = 0;
    const walker = document.createTreeWalker(card, NodeFilter.SHOW_TEXT);
    const take = (r) => { if (r.width < 1 || r.height < 1) return; n++; top = Math.min(top, r.top); bottom = Math.max(bottom, r.bottom); left = Math.min(left, r.left); right = Math.max(right, r.right); };
    for (let t = walker.nextNode(); t; t = walker.nextNode()) { if (!t.textContent.trim()) continue; const rg = document.createRange(); rg.selectNodeContents(t); take(rg.getBoundingClientRect()); }
    for (const c of card.querySelectorAll('input,select,textarea,button,img,svg,canvas,progress,meter')) if (vis(c)) take(c.getBoundingClientRect());
    if (!n) { if (box.height > 6 * rem) problems.push(`card with nothing in it takes ${Math.round(box.height)}px: ${label(card)}`); continue; }
    const vfill = (bottom - top) / box.height;
    if (box.height > 6 * rem && vfill < 0.5) problems.push(`dead space: content fills ${Math.round(vfill * 100)}% of the height of ${label(card)} (${Math.round(box.height)}px)`);
  }
  return problems;
}

const executablePath = process.env.CHROME || undefined;
const browser = await chromium.launch({ executablePath });
let bad = 0;
if (shotsDir) fs.mkdirSync(shotsDir, { recursive: true });
for (const c of cases) {
  let body;
  try { body = draw(c.env); } catch (e) { console.log(`FAIL ${c.name}: ${e.message}`); bad++; continue; }
  const html = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><style>${style}</style></head><body>${body}</body></html>`;
  for (const [vw, vh] of [[1440, 900], [390, 844]]) {
    const ctx = await browser.newContext({ viewport: { width: vw, height: vh }, colorScheme: 'light' });
    const page = await ctx.newPage();
    await page.setContent(html, { waitUntil: 'load' });
    const found = await page.evaluate(inspect);
    if (shotsDir) await page.screenshot({ path: path.join(shotsDir, `${c.name}-${vw}.jpg`), fullPage: true, type: 'jpeg', quality: 70 });
    for (const p of found) { console.log(`FAIL ${c.name} @${vw}: ${p}`); bad++; }
    await ctx.close();
  }
}
await browser.close();
console.log(bad ? `${bad} layout problems in ${cases.length} pages` : `layout ok: ${cases.length} pages at 1440 and 390`);
process.exit(bad ? 1 : 0);
