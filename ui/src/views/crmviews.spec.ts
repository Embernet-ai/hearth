import { describe, expect, it } from 'vitest';
import { draw } from './testing/draw';
import { fixture } from './testing/fixture';

const words = (e: Element) => (e.textContent ?? '').replace(/\s+/g, ' ').trim();

describe('CRM lists: grouping and saved views', () => {
  const d = draw('companies', fixture('companies-grouped'));

  it('puts the rows under headings with each heading\'s total across the whole list', () => {
    const heads = [...d.querySelectorAll('tr.grouprow')].map((r) => words(r));
    expect(heads).toEqual(['vendor 12', 'customer 4']);
    // the heading sits above its first row only: three rows, two headings
    expect(d.querySelectorAll('tr[data-company]').length).toBe(3);
    const rows = [...d.querySelectorAll('tbody tr')].map((r) => r.className === 'grouprow' ? 'H' : 'R').join('');
    expect(rows).toBe('HRRHR');
  });

  it('offers the grouping choices and keeps the chosen one selected', () => {
    const sel = d.querySelector('select[name=group]') as HTMLSelectElement;
    expect([...sel.querySelectorAll('option')].map((o) => o.getAttribute('value'))).toEqual(['', 'kind', 'owner']);
    expect(sel.querySelector('option[selected]')?.getAttribute('value')).toBe('kind');
  });

  it('lists the saved views, own ones removable and shared ones named for their owner', () => {
    const bar = d.querySelector('[data-crm-views]')!;
    const links = [...bar.querySelectorAll('a.chip')].map((a) => [a.getAttribute('href'), words(a)]);
    expect(links).toEqual([['/companies?kind=vendor&group=owner', 'Vendors by owner'], ['/companies?group=kind', 'Open accounts · Aria']]);
    const removes = [...bar.querySelectorAll('form[action$="/delete"]')].map((f) => f.getAttribute('action'));
    expect(removes).toEqual(['/crm/views/v1/delete']);
    const save = bar.querySelector('form[action="/crm/views"]')!;
    expect(save.querySelector('input[name=from]')?.getAttribute('value')).toBe('/companies?group=kind');
    expect(save.querySelector('input[name=name]')).not.toBeNull();
    expect(save.querySelector('input[name=shared]')).not.toBeNull();
  });
});
