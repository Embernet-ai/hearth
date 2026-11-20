import { describe, expect, it } from 'vitest';
import { draw } from './testing/draw';
import { fixture } from './testing/fixture';

const words = (e: Element) => (e.textContent ?? '').replace(/\s+/g, ' ').trim();

describe('CRM favorites', () => {
  it('lists the starred records with where each one goes', () => {
    const d = draw('crmfavorites', fixture('crmfavorites'));
    const rows = [...d.querySelectorAll('table[data-favorites] tbody tr')].map((r) => [r.querySelector('a')?.getAttribute('href'), words(r)]);
    expect(rows).toEqual([
      ['/companies/c1', 'Baldor Electric Company vendor'],
      ['/contacts/k1', 'Ada Lovelace Contact Acme Motors'],
      ['/deals/d1', 'Press brake retrofit Deal Proposal $12,500.00'],
    ]);
  });

  it('a starred record offers to remove the star and an unstarred one offers to add it', () => {
    const on = draw('company', fixture('company-favorite')).querySelector('form[data-favorite]')!;
    expect(on.getAttribute('data-favorite')).toBe('on');
    expect(on.querySelector('input[name=on]')?.getAttribute('value')).toBe('0');
    expect(on.querySelector('input[name=id]')?.getAttribute('value')).toBe('c1');
    expect(on.querySelector('input[name=type]')?.getAttribute('value')).toBe('company');
    expect(on.querySelector('button')?.getAttribute('aria-pressed')).toBe('true');
    const off = draw('company', fixture('company-plain')).querySelector('form[data-favorite]')!;
    expect(off.getAttribute('data-favorite')).toBe('off');
    expect(off.querySelector('input[name=on]')?.getAttribute('value')).toBe('1');
    expect(off.querySelector('button')?.getAttribute('aria-pressed')).toBe('false');
  });
});
