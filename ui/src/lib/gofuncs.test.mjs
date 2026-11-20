// Holds the TypeScript ports of Go's template functions to the answers Go gave (gofuncs.golden.json, written while the Go
// templates still ran and kept as the record of what each function prints). Run with: node --test src/lib/*.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { transform } from 'esbuild';

const src = fs.readFileSync(new URL('./gofuncs.ts', import.meta.url), 'utf8');
const { code } = await transform(src, { loader: 'ts', format: 'esm' });
const mod = await import('data:text/javascript;base64,' + Buffer.from(code).toString('base64'));
const F = mod.F;
const golden = JSON.parse(fs.readFileSync(new URL('./gofuncs.golden.json', import.meta.url), 'utf8'));

test('every golden case matches Go', () => {
  const bad = [];
  for (const c of golden) {
    let got;
    try {
      got = c.fn === 'Format' ? F.m(c.args[0], 'Format', c.args[1])
        : c.fn === 'Has' ? F.m(c.args[0], 'Has', c.args[1], c.args[2])
        : F[c.fn](...c.args);
    } catch (e) { got = 'THROWS ' + e.message; }
    // Go sends nil as null and an absent time as the zero time; the port treats both as "nothing".
    if (JSON.stringify(got) !== JSON.stringify(c.want)) bad.push(`${c.fn}(${JSON.stringify(c.args).slice(0, 90)}) want ${JSON.stringify(c.want)} got ${JSON.stringify(got)}`);
  }
  assert.equal(bad.length, 0, `${bad.length} of ${golden.length} differ:\n` + bad.slice(0, 25).join('\n'));
});
