import { describe, expect, it } from 'vitest';
import { draw } from './testing/draw';
import { fixture } from './testing/fixture';

const words = (e: Element) => (e.textContent ?? '').replace(/\s+/g, ' ').trim();
const attr = (e: Element | null, a: string) => e?.getAttribute(a);

describe('time is entered in hours and minutes', () => {
  const d = draw('document', fixture('document-time-refused'));
  const form = d.querySelector('form[data-time-form]')!;

  it('has two whole-number fields side by side that open the number keypad, and no free-text duration', () => {
    const h = form.querySelector('input[name=hours]')!, m = form.querySelector('input[name=mins]')!;
    expect(attr(h, 'type')).toBe('number');
    expect(attr(h, 'inputmode')).toBe('numeric');
    expect(attr(m, 'inputmode')).toBe('numeric');
    expect(attr(m, 'min')).toBe('0');
    expect(attr(m, 'max')).toBe('59');
    expect(h.compareDocumentPosition(m) & 4).toBeTruthy(); // minutes follows hours
    expect(h.closest('.hm')).toBe(m.closest('.hm'));
    expect(form.querySelector('input[name=minutes]')).toBeNull();
    expect(form.querySelector('[list=durations]')).toBeNull();
  });

  it('shows the reason beside the fields and says time was not logged at the top of the page', () => {
    expect(words(form.querySelector('#minutes-error')!)).toContain('Minutes go from 0 to 59');
    expect(attr(form.querySelector('input[name=mins]'), 'aria-describedby')).toBe('minutes-error');
    expect(attr(form.querySelector('input[name=mins]'), 'aria-invalid')).toBe('true');
    expect(words(d.querySelector('[data-time-error]')!)).toContain('Time was not logged');
  });

  it('keeps what was typed: hours, minutes, the day, the note and the ticked tags', () => {
    expect(attr(form.querySelector('input[name=hours]'), 'value')).toBe('1');
    expect(attr(form.querySelector('input[name=mins]'), 'value')).toBe('75');
    expect(attr(form.querySelector('input[name=on]'), 'value')).toBe('2026-11-18');
    expect(attr(form.querySelector('input[name=note]'), 'value')).toBe('wired the panel');
    const ticked = [...form.querySelectorAll('input[name=tags]')].map((i) => [i.getAttribute('value'), i.hasAttribute('checked')]);
    expect(ticked).toEqual([['t1', false], ['t2', true]]);
  });

  it('is a plain form when nothing was refused', () => {
    const plain = draw('document', { ...fixture('document-time-refused'), TimeForm: null });
    expect(plain.querySelector('#minutes-error')).toBeNull();
    expect(plain.querySelector('[data-time-error]')).toBeNull();
    expect(attr(plain.querySelector('form[data-time-form] input[name=hours]'), 'value') ?? '').toBe('');
  });
});

describe('correcting an entry', () => {
  it('pre-fills both fields from the stored total', () => {
    const d = draw('document', fixture('document-time-fix'));
    const rows = [...d.querySelectorAll('li[data-entry]')];
    const values = rows.map((li) => [attr(li.querySelector('input[name=hours]'), 'value'), attr(li.querySelector('input[name=mins]'), 'value')]);
    // e1 is 90 minutes, e2 was refused with both left blank, so what was typed (nothing) is kept
    expect(values[0]).toEqual(['1', '30']);
    expect(values[1]).toEqual(['', '']);
  });

  it('shows a refused correction beside that entry only', () => {
    const d = draw('document', fixture('document-time-fix'));
    const errs = [...d.querySelectorAll('li[data-entry]')].map((li) => li.querySelector('.error') ? words(li.querySelector('.error')!) : null);
    expect(errs[0]).toBeNull();
    expect(errs[1]).toBe('Enter how long it took: hours, minutes, or both.');
  });

  it('pre-fills an entry of 45 minutes as 0 hours and 45 minutes', () => {
    const d = draw('document', { ...fixture('document-time-fix'), TimeForm: null });
    const li = d.querySelectorAll('li[data-entry]')[1];
    expect([attr(li.querySelector('input[name=hours]'), 'value'), attr(li.querySelector('input[name=mins]'), 'value')]).toEqual(['0', '45']);
  });
});

describe('the estimate', () => {
  it('is read back as hours and minutes', () => {
    const d = draw('document', fixture('document-estimate-set'));
    expect(attr(d.querySelector('input[name=estimate_hours]'), 'value')).toBe('3');
    expect(attr(d.querySelector('input[name=estimate_mins]'), 'value')).toBe('15');
  });
});
