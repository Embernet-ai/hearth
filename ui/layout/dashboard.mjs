// Dashboard dead-space check. Draws the dashboard with the real views and stylesheets at ten widths and fails on
//   - sideways scrolling,
//   - columns of the dashboard that end far apart (a short column leaves an empty region under it),
//   - a block with more than a small gap between its last content and its bottom edge,
//   - a tile grid whose last row is not full (an orphan tile leaves a hole beside it),
//   - a column that does not start at the top (empty band above a block).
// usage: npm run build && npm run build:ssr && node layout/dashboard.mjs [--shots DIR] [--fixture name]
import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { chromium } from 'playwright';

const root = path.resolve(import.meta.dirname, '..');
const args = process.argv.slice(2);
const opt = (k, d) => (args.includes(k) ? args[args.indexOf(k) + 1] : d);
const shots = opt('--shots', null);
const fixtures = (opt('--fixture', 'dashboard-full')).split(',');
const WIDTHS = [390, 768, 1024, 1280, 1440, 1680, 1920, 2048, 2560, 3840];
// columns may end this far apart, in px; larger text packs fewer blocks into a column, so the stress run allows more
const SPREAD = Number(opt('--spread', process.env.FONT_STRESS === '1' ? 24 * 18 : 14 * 16));
const BLOCK_GAP = 28; // empty strip allowed under the last thing in a block

const css = [fs.readFileSync(path.join(root, '..', 'internal/web/static/hearth.css'), 'utf8')];
for (const f of fs.readdirSync(path.join(root, 'dist/_astro')).filter((f) => f.endsWith('.css')).sort()) css.push(fs.readFileSync(path.join(root, 'dist/_astro', f), 'utf8'));
// FONT_STRESS=1 draws the page with larger, wider text, as check.mjs does, so a layout that only just fits does not pass here and break on a phone
const stress = process.env.FONT_STRESS === '1' ? `\nhtml{font-size:${process.env.STRESS_SIZE || '112.5%'}} *{letter-spacing:.02em}` : '';
const style = css.join('\n').replace(/url\(\/_astro\/[^)]*\)/g, 'url()') + stress;
const base = { Title: '', Path: '/', Version: '', Message: '', Today: '2026-11-24', Identity: { PrincipalID: 'me', OrgID: 'o', DisplayName: 'Patrick Ryan', Subjects: [], MustChangePassword: false }, Build: { Revision: 'test', Profile: 'test' }, Can: {}, IsAdmin: false, SalesNav: true };

function inspect({ spread, gap }) {
  const out = [];
  const main = document.querySelector('main');
  if (document.documentElement.scrollWidth > innerWidth + 1) out.push('the page scrolls sideways');
  const cols = document.querySelector('.dashcols');
  if (!cols) return ['no .dashcols on the page'];
  const blocks = [...cols.querySelectorAll(':scope > .numgroup, :scope > .dashblock, :scope .numbers > header, :scope .numgrid > .numgroup, :scope .numbers > .numgrid > .numgroup')].filter((e, i, a) => a.indexOf(e) === i);
  // a block that splits between columns is one box in each: every fragment counts in the column it sits in
  const rects = blocks.flatMap((b) => [...b.getClientRects()].map((r) => ({ b, r }))).filter((x) => x.r.width > 20 && x.r.height > 10);
  const top = Math.min(...rects.map((x) => x.r.top));
  const byLeft = new Map();
  for (const x of rects) {
    const k = Math.round(x.r.left / 8);
    if (!byLeft.has(k)) byLeft.set(k, []);
    byLeft.get(k).push(x);
  }
  const bottoms = [];
  for (const [, list] of byLeft) {
    const first = Math.min(...list.map((x) => x.r.top));
    const last = Math.max(...list.map((x) => x.r.bottom));
    bottoms.push(last);
    if (first - top > 24) out.push(`a column starts ${Math.round(first - top)}px below the top`);
  }
  if (byLeft.size > 1) {
    const s = Math.max(...bottoms) - Math.min(...bottoms);
    if (s > spread) out.push(`columns end ${Math.round(s)}px apart (limit ${spread}px): ${bottoms.map(Math.round).join(', ')}`);
  }
  // a block with an empty strip under its content
  for (const { b, r } of rects) {
    if (b.getClientRects().length > 1) continue; // the end of a split list is the column's own bottom
    let low = r.top;
    for (const c of b.querySelectorAll('*')) {
      const cr = c.getBoundingClientRect();
      if (cr.width > 1 && cr.height > 1 && getComputedStyle(c).visibility !== 'hidden') low = Math.max(low, cr.bottom);
    }
    if (r.bottom - low > gap && r.height > 80) out.push(`a ${b.className.split(' ')[0]} has ${Math.round(r.bottom - low)}px of empty space under its content`);
  }
  // tile grids: the last row is as full as the others
  for (const g of main.querySelectorAll('.stats')) {
    const tiles = [...g.children].map((t) => t.getBoundingClientRect()).filter((t) => t.width > 1);
    const rows = new Map();
    for (const t of tiles) rows.set(Math.round(t.top / 4), (rows.get(Math.round(t.top / 4)) ?? 0) + 1);
    const counts = [...rows.values()];
    if (counts.length > 1 && counts[counts.length - 1] < counts[0]) out.push(`a tile grid ends on a row of ${counts[counts.length - 1]} where the others hold ${counts[0]}`);
  }
  return out;
}

if (shots) fs.mkdirSync(shots, { recursive: true });
const browser = await chromium.launch({ executablePath: process.env.CHROME || undefined });
let bad = 0;
for (const name of fixtures) {
  const data = JSON.parse(fs.readFileSync(path.join(root, 'src/views/fixtures', name + '.json'), 'utf8'));
  const page = { ...base, ...data, Identity: base.Identity, Build: base.Build, Can: base.Can };
  const r = spawnSync('node', [path.join(root, 'dist-ssr/render.mjs')], { input: JSON.stringify({ template: 'dashboard', page, assets: { css: [], scripts: [] } }), maxBuffer: 1 << 28 });
  if (r.status !== 0) throw new Error('render failed: ' + r.stderr.toString().slice(0, 300));
  const html = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><style>${style}</style></head><body>${r.stdout}</body></html>`;
  for (const w of WIDTHS) {
    const ctx = await browser.newContext({ viewport: { width: w, height: Math.round(w * 0.56) }, colorScheme: 'light' });
    const p = await ctx.newPage();
    await p.setContent(html, { waitUntil: 'load' });
    const found = await p.evaluate(inspect, { spread: SPREAD, gap: BLOCK_GAP });
    if (shots) await p.screenshot({ path: path.join(shots, `${name}-${w}.png`), fullPage: true });
    if (found.length) { bad++; console.log(`FAIL ${name} at ${w}px:\n  ${found.join('\n  ')}`); } else console.log(`ok   ${name} at ${w}px`);
    await ctx.close();
  }
}
await browser.close();
if (bad) { console.log(`${bad} widths have dead space`); process.exit(1); }
console.log('dashboard layout ok: no dead space at ' + WIDTHS.join(', '));
