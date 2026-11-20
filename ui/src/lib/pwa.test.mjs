// The page script that installs the service worker (internal/web/static/pwa.js) is added to the page by the interface after the
// page's data has been fetched, which can be after the window's load event. Registering only on load meant that, on those pages,
// the worker was never installed and a phone with no signal had nothing to open. This runs the real script against a stand-in browser.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

const source = fs.readFileSync(new URL('../../../internal/web/static/pwa.js', import.meta.url), 'utf8');

function run(readyState) {
  const registered = [];
  const windowListeners = {};
  const sw = {
    register: (url, opts) => { registered.push([url, opts]); return Promise.resolve({ update() {} }); },
    addEventListener() {},
    controller: null,
  };
  const noop = () => {};
  const element = () => ({ addEventListener: noop, setAttribute: noop, appendChild: noop, querySelector: () => null, querySelectorAll: () => [], classList: { add: noop, remove: noop, toggle: noop } });
  const sandbox = {
    navigator: { serviceWorker: sw, onLine: true },
    window: { addEventListener: (ev, fn) => { (windowListeners[ev] ??= []).push(fn); }, matchMedia: () => ({ matches: false, addEventListener: noop }) },
    document: { readyState, addEventListener: noop, querySelector: () => null, querySelectorAll: () => [], getElementById: () => null, createElement: element, body: element(), documentElement: element(), visibilityState: 'visible' },
    localStorage: { getItem: () => null, setItem: noop },
    console, setTimeout, clearTimeout, indexedDB: undefined,
  };
  sandbox.window.localStorage = sandbox.localStorage;
  vm.runInNewContext(source, sandbox);
  return { registered, fire: (ev) => (windowListeners[ev] || []).forEach((f) => f()) };
}

test('a page that has already finished loading installs the worker at once', () => {
  const r = run('complete');
  assert.equal(r.registered.length, 1, 'the load event has gone by, so waiting for it never installs the worker');
  assert.equal(r.registered[0][0], '/sw.js');
  assert.equal(r.registered[0][1].scope, '/');
});

test('a page still loading installs the worker when it has loaded, not before', () => {
  const r = run('loading');
  assert.equal(r.registered.length, 0, 'registering must not compete with the page being drawn');
  r.fire('load');
  assert.equal(r.registered.length, 1);
});
