// Time report layout check. Draws the per-person page and the totals page with the real views and stylesheets at the widths a
// person reads them at, and fails on
//   - sideways scrolling of the page,
//   - a filter (its label or its control) that is not fully on screen,
//   - a row that a long note turned into a block (taller than a note's first lines),
//   - the People selector open with anything off the screen or a list that does not scroll in itself,
//   - a note that is not the width of the table, or runs off its edge.
// usage: npm run build && npm run build:ssr && node layout/timereport.mjs [--shots DIR]
import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { chromium } from 'playwright';

const root = path.resolve(import.meta.dirname, '..');
const args = process.argv.slice(2);
const shots = args.includes('--shots') ? args[args.indexOf('--shots') + 1] : null;
const WIDTHS = [390, 768, 1024, 1280, 1366, 1440, 1920, 2560];
const css = [fs.readFileSync(path.join(root, '..', 'internal/web/static/hearth.css'), 'utf8')];
for (const f of fs.readdirSync(path.join(root, 'dist/_astro')).filter((f) => f.endsWith('.css')).sort()) css.push(fs.readFileSync(path.join(root, 'dist/_astro', f), 'utf8'));
const stress = process.env.FONT_STRESS === '1' ? `\nhtml{font-size:${process.env.STRESS_SIZE || '112.5%'}} *{letter-spacing:.02em}` : '';
const style = css.join('\n').replace(/url\(\/_astro\/[^)]*\)/g, 'url()') + stress;
const base = { Title: '', Path: '/', Version: '', Message: '', Today: '2026-11-27', Identity: { PrincipalID: 'me', OrgID: 'o', DisplayName: 'Yessid', Subjects: [], MustChangePassword: false }, Build: { Revision: 'test', Profile: 'test' }, Can: {}, IsAdmin: false, SalesNav: true };

function inspect() {
  const out = [];
  if (document.documentElement.scrollWidth > innerWidth + 1) out.push(`the page scrolls sideways (${document.documentElement.scrollWidth}px in ${innerWidth}px)`);
  const form = document.querySelector('form.reportfilters');
  if (!form) return ['no filter bar'];
  for (const el of form.querySelectorAll('label, input:not([type=hidden]), select, button, a.chip')) {
    const r = el.getBoundingClientRect();
    if (r.width < 1) continue;
    if (r.left < -1 || r.right > innerWidth + 1) out.push(`a filter part is off the screen: ${el.tagName.toLowerCase()} "${(el.textContent || el.id || '').trim().slice(0, 20)}" ends at ${Math.round(r.right)}px of ${innerWidth}px`);
  }
  for (const id of ['from', 'to', 'f-approval']) {
    const c = document.getElementById(id), l = document.querySelector(`label[for=${id}]`);
    if (!c || !l) out.push(`filter #${id} lost its label or its control`);
  }
  // a note is cut at a paragraph, so its row is a few lines; the entry's own row is the same height with or without one
  const noteRows = [...document.querySelectorAll('table.list tbody tr.noterow')].map((r) => r.getBoundingClientRect().height);
  const tall = Math.max(0, ...noteRows);
  if (tall > (innerWidth < 600 ? 230 : 120)) out.push(`a note row is ${Math.round(tall)}px tall: a long note is stretching it`);
  const table = document.querySelector('table.list');
  const notes = [...document.querySelectorAll('table.list tr.noterow > td')];
  if (table && document.querySelector('table.list tr.hasnote') && notes.length === 0) out.push('an entry with a note has no note row');
  for (const n of notes) {
    const w = n.getBoundingClientRect().width, tw = table.getBoundingClientRect().width;
    if (w < tw * 0.6) out.push(`a note is ${Math.round(w)}px wide in a ${Math.round(tw)}px table`);
    if (n.getBoundingClientRect().right > innerWidth + 1) out.push('a note runs off the right edge');
  }
  const wide = table && table.getBoundingClientRect().right > innerWidth + 1 && !table.closest('.table-scroll');
  if (wide) out.push('the table runs off the page');
  return out;
}

if (shots) fs.mkdirSync(shots, { recursive: true });
const browser = await chromium.launch({ executablePath: process.env.CHROME || undefined });
let bad = 0;
for (const name of ['timereport-person-approval', 'timereport-filtered']) {
  const data = JSON.parse(fs.readFileSync(path.join(root, 'src/views/fixtures', name + '.json'), 'utf8'));
  const page = { ...base, ...data, Identity: base.Identity, Build: base.Build, Can: base.Can };
  const r = spawnSync('node', [path.join(root, 'dist-ssr/render.mjs')], { input: JSON.stringify({ template: 'timereport', page, assets: { css: [], scripts: [] } }), maxBuffer: 1 << 28 });
  if (r.status !== 0) throw new Error('render failed: ' + r.stderr.toString().slice(0, 300));
  const html = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><style>${style}</style></head><body>${r.stdout}</body></html>`;
  for (const w of WIDTHS) {
    const ctx = await browser.newContext({ viewport: { width: w, height: 800 }, colorScheme: 'light' });
    const p = await ctx.newPage();
    await p.setContent(html, { waitUntil: 'load' });
    let found = await p.evaluate(inspect);
    // the People selector open: its whole list on screen, scrolling inside itself, nothing off the edge
    if (await p.locator('details.whopick').count()) {
      await p.locator('details.whopick > summary').click();
      const open = await p.evaluate(() => {
        const out = [];
        if (document.documentElement.scrollWidth > innerWidth + 1) out.push('the page scrolls sideways with the People selector open');
        const panel = document.querySelector('details.whopick .whopanel');
        const r = panel.getBoundingClientRect();
        if (r.left < -1 || r.right > innerWidth + 1) out.push(`the open People list is off the screen (${Math.round(r.left)} to ${Math.round(r.right)} of ${innerWidth}px)`);
        for (const el of panel.querySelectorAll('input[type=search], button, .wholist label')) {
          const q = el.getBoundingClientRect();
          if (q.width > 1 && (q.right > innerWidth + 1 || q.left < -1) && !el.closest('.wholist')) out.push(`a People control is off the screen: ${(el.textContent || el.placeholder || '').trim().slice(0, 20)}`);
        }
        const list = panel.querySelector('.wholist');
        if (list.scrollHeight > list.clientHeight + 1 && getComputedStyle(list).overflowY === 'visible') out.push('a long People list does not scroll inside itself');
        const summary = document.querySelector('details.whopick > summary').textContent.trim();
        if (!summary) out.push('the People selector says nothing collapsed');
        return out;
      });
      found = found.concat(open);
      if (shots) await p.screenshot({ path: path.join(shots, `${name}-open-${w}.png`), fullPage: false });
    }
    if (shots) await p.screenshot({ path: path.join(shots, `${name}-${w}.png`), fullPage: false });
    if (found.length) { bad++; console.log(`FAIL ${name} at ${w}px:\n  ${found.join('\n  ')}`); } else console.log(`ok   ${name} at ${w}px`);
    await ctx.close();
  }
}
await browser.close();
if (bad) { console.log(`${bad} widths fail`); process.exit(1); }
console.log('time report layout ok at ' + WIDTHS.join(', '));
