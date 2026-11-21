import { describe, expect, test } from 'vitest';
import { draw, text } from './testing/draw';
import { fixture } from './testing/fixture';

describe('a folder\'s access page', () => {
  // A manager names a person, a level and an optional last day on the folder's own page, and each person who holds it can be taken back.
  test('gives access to one folder and takes it back, in place', () => {
    const d = draw('spaceaccess', fixture('spaceaccess'));
    const t = text(d);
    for (const want of ['Access to Alpha', 'Dept / Projects / Alpha', 'Give access', 'Who has it', 'Carol', 'contractor', 'Dan', 'ended', 'Eve', 'no end date']) {
      expect(t, `access page is missing ${want}`).toContain(want);
    }
    const give = d.querySelector('form[action="/spaces/alpha/access"]');
    expect(give).not.toBeNull();
    expect(give!.querySelector('input[name=email][type=email][required]')).not.toBeNull();
    expect(give!.querySelector('input[name=expires][type=date]')).not.toBeNull();
    expect([...give!.querySelectorAll('select[name=level] option')].map((o) => o.getAttribute('value'))).toEqual(['view', 'comment', 'edit']);
    for (const p of ['p1', 'p2', 'p3']) {
      expect(d.querySelector(`form[action="/spaces/alpha/access/${p}/remove"]`), `no take back for ${p}`).not.toBeNull();
    }
  });

  test('says why when a person cannot be found, and shows nobody when nobody holds it', () => {
    const d = draw('spaceaccess', fixture('spaceaccess-empty'));
    expect(text(d)).toContain('Nobody here signs in with that address');
    expect(text(d)).toContain('Nobody has been given this folder on its own');
    expect(d.querySelectorAll('form[action*="/remove"]')).toHaveLength(0);
  });
});
