// The browser half of turning push on: the key conversion has to match what the server's VAPID public key is (65 bytes,
// URL-safe base64), the device name has to come from the user agent, and an iPhone outside the Home Screen has to be told how.
import test from 'node:test';
import assert from 'node:assert/strict';
import { keyBytes, deviceLabel, whyNot } from './push.ts';

test('a VAPID public key (URL-safe, unpadded) becomes its 65 bytes', () => {
  const raw = new Uint8Array(65).map((_, i) => (i === 0 ? 4 : i * 3 + 250));
  const b64 = Buffer.from(raw).toString('base64url');
  assert.deepEqual([...keyBytes(b64)], [...raw]);
});

test('devices are named from the user agent', () => {
  assert.equal(deviceLabel('Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1'), 'iPhone Safari');
  assert.equal(deviceLabel('Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36'), 'Android Chrome');
  assert.equal(deviceLabel('Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0'), 'Windows Edge');
});

test('an iPhone that has not added the app to the Home Screen is told to', () => {
  const ua = 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148 Safari/604.1';
  assert.match(whyNot({ sw: true, push: false, notification: false, ua, standalone: false }), /Home Screen/);
  assert.equal(whyNot({ sw: true, push: true, notification: true, ua, standalone: true }), '');
});
