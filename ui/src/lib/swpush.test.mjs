// The service worker's push half, run for real against a stand-in worker scope: a push shows a notification (the browsers
// withdraw a subscription that ever receives one and shows nothing), and a click opens a path on this site and nothing else.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

const source = fs.readFileSync(new URL('../../../internal/web/static/sw.js', import.meta.url), 'utf8').replace('__BUILD__', 'test');

function worker(windows = []) {
  const handlers = {};
  const shown = [];
  const opened = [];
  const focused = [];
  const self = {
    location: { origin: 'https://hearth.test' },
    addEventListener: (ev, fn) => { handlers[ev] = fn; },
    skipWaiting() {},
    registration: { showNotification: (title, o) => { shown.push([title, o]); return Promise.resolve(); } },
    clients: {
      claim: () => Promise.resolve(),
      matchAll: () => Promise.resolve(windows.map((url) => ({ url, focus: () => { focused.push(url); return Promise.resolve(); }, navigate: (u) => { opened.push(['navigate', u]); return Promise.resolve(); } }))),
      openWindow: (u) => { opened.push(['open', u]); return Promise.resolve(); },
    },
  };
  vm.runInNewContext(source, { self, caches: { open: () => Promise.resolve({ addAll() {} }), keys: () => Promise.resolve([]), match() {} }, console, URL, Request: class {}, Response: class {}, indexedDB: undefined, fetch() {} });
  const run = async (ev, evt) => { let p; handlers[ev]({ ...evt, waitUntil: (x) => { p = x; } }); await p; };
  return { run, shown, opened, focused };
}

test('a push shows a notification with the sentence, the tag and where it goes', async () => {
  const w = worker();
  await w.run('push', { data: { json: () => ({ title: 'Hearth', body: 'Sam assigned you Replace the bearing', url: '/documents/abc', tag: 'hearth-document-abc' }) } });
  assert.equal(w.shown.length, 1);
  assert.equal(w.shown[0][0], 'Hearth');
  assert.equal(w.shown[0][1].body, 'Sam assigned you Replace the bearing');
  assert.equal(w.shown[0][1].tag, 'hearth-document-abc');
  assert.equal(w.shown[0][1].data.url, '/documents/abc');
});

test('a push the worker cannot read still shows a notification', async () => {
  const w = worker();
  await w.run('push', { data: { json: () => { throw new Error('not json'); }, text: () => 'plain words' } });
  assert.equal(w.shown.length, 1);
  assert.equal(w.shown[0][1].body, 'plain words');
  const none = worker();
  await none.run('push', {});
  assert.equal(none.shown.length, 1);
});

test('a click opens the item in the open app, or a new window when there is none', async () => {
  const open = worker(['https://hearth.test/inbox']);
  await open.run('notificationclick', { notification: { close() {}, data: { url: '/documents/abc' } } });
  assert.deepEqual(open.focused, ['https://hearth.test/inbox']);
  assert.deepEqual(open.opened, [['navigate', '/documents/abc']]);
  const closed = worker();
  await closed.run('notificationclick', { notification: { close() {}, data: { url: '/time-off#r-9' } } });
  assert.deepEqual(closed.opened, [['open', '/time-off#r-9']]);
});

test('a click never leaves this site', async () => {
  for (const bad of ['https://evil.example/x', '//evil.example/x', '/\\evil.example', 'javascript:alert(1)', undefined, 7]) {
    const w = worker();
    await w.run('notificationclick', { notification: { close() {}, data: { url: bad } } });
    assert.deepEqual(w.opened, [['open', '/inbox']], String(bad));
  }
});
