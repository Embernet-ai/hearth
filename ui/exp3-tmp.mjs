import fs from 'node:fs'; import path from 'node:path'; import { spawnSync } from 'node:child_process'; import { chromium } from 'playwright';
const root = process.cwd();
const css = [fs.readFileSync(path.join(root, '..', 'internal/web/static/hearth.css'), 'utf8')];
for (const f of fs.readdirSync(path.join(root, 'dist/_astro')).filter((f) => f.endsWith('.css')).sort()) css.push(fs.readFileSync(path.join(root, 'dist/_astro', f), 'utf8'));
const style = css.join('\n').replace(/url\(\/_astro\/[^)]*\)/g, 'url()');
const base = { Title: '', Path: '/', Version: '', Message: '', Today: '2026-11-24', Identity: { PrincipalID: 'me', OrgID: 'o', DisplayName: 'P', Subjects: [], MustChangePassword: false }, Build: { Revision: 'test', Profile: 'test' }, Can: {}, IsAdmin: false, SalesNav: true };
const data = JSON.parse(fs.readFileSync('src/views/fixtures/dashboard-full.json', 'utf8'));
const r = spawnSync('node', ['dist-ssr/render.mjs'], { input: JSON.stringify({ template: 'dashboard', page: { ...base, ...data, Identity: base.Identity, Build: base.Build, Can: base.Can }, assets: { css: [], scripts: [] } }), maxBuffer: 1 << 28 });
const b = await chromium.launch({ executablePath: '/usr/bin/chromium' });
const html = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><style>${style}</style></head><body>${r.stdout}</body></html>`;
const widths = [1280, 1440, 1680, 1920, 2048, 2560, 3840];
const pages = [];
for (const w of widths) { const ctx = await b.newContext({ viewport: { width: w, height: 900 } }); const p = await ctx.newPage(); await p.setContent(html); pages.push(p); }
const units = ['N', 'R', 'dashwork', 'dashwhatson', 'monthcard', 'dashtime', 'dashfollow'];
const shuffle = (a) => { a = [...a]; for (let i = a.length - 1; i > 0; i--) { const j = Math.floor(Math.random() * (i + 1)); [a[i], a[j]] = [a[j], a[i]]; } return a; };
const t0 = Date.now(); const results = [];
const seen = new Set();
while (Date.now() - t0 < 420000) {
  const order = shuffle(units); const key = order.join(','); if (seen.has(key)) continue; seen.add(key);
  let worst = 0; const per = [];
  for (const p of pages) {
    const s = await p.evaluate((order) => {
      const c = document.querySelector('.dashcols');
      const sel = (u) => u === 'N' ? c.querySelector('.numbers:not(.recap)') : u === 'R' ? c.querySelector('.numbers.recap') : c.querySelector('.' + u);
      for (const u of order) { const e = sel(u); if (e) c.appendChild(e); }
      const bl = [...document.querySelectorAll('.dashcols .numgroup, .dashcols .dashblock')].flatMap((e) => [...e.getClientRects()]); const m = new Map();
      for (const r of bl) { const k = Math.round(r.left / 8); m.set(k, Math.max(m.get(k) ?? 0, r.bottom)); }
      const v = [...m.values()]; return m.size > 1 ? Math.round(Math.max(...v) - Math.min(...v)) : 0; }, order);
    per.push(s); worst = Math.max(worst, s);
  }
  results.push([worst, key, per.join('/')]);
}
results.sort((a, b) => a[0] - b[0]);
console.log(results.length + ' orders tried');
console.log(results.slice(0, 5).map((x) => x.join(' ')).join('\n'));
await b.close();
