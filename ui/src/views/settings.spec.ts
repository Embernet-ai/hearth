import { describe, expect, test } from 'vitest';
import { draw } from './testing/draw';
import { fixture } from './testing/fixture';

// The Settings page is open to everybody, so the Administration section inside it is the only thing standing between an
// ordinary account and links to pages it cannot open. Each group has to appear for exactly the grant it needs.
const admin = ['/people', '/rules', '/audit', '/reports/time', '/time/imported'];

describe('settings', () => {
  const cases: [string, string[]][] = [
    ['settings-nobody', []],
    ['settings-finance', ['/reports/time', '/time/imported']],
    ['settings-owner', admin],
  ];
  test.each(cases)('%s', (name, want) => {
    const d = draw('settings', fixture(name));
    expect(d.querySelector('a[href="/account"]'), 'personal settings missing').not.toBeNull();
    for (const link of admin) {
      expect(d.querySelector(`a[href="${link}"]`) !== null, `${link} shown`).toBe(want.includes(link));
    }
    expect([...d.querySelectorAll('h2')].some((h) => h.textContent === 'Administration')).toBe(want.length > 0);
  });
});
