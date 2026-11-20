import { describe, expect, test } from 'vitest';
import { draw, text } from './testing/draw';
import { fixture } from './testing/fixture';

const words = (e: Element) => (e.textContent ?? '').replace(/\s+/g, ' ').trim();
const attr = (e: Element | null, a: string) => e?.getAttribute(a);

describe('time off for an approver or an admin', () => {
  const d = draw('timeoff', fixture('timeoff-decide'));
  const item = (id: string) => d.querySelector(`#r-${id}`)!;

  test('a pending request keeps Approve and Deny and can be changed or cancelled', () => {
    const li = item('t1');
    expect(li.querySelector('form[action="/time-off/t1/decide"] button[name=approve][value="1"]')).not.toBeNull();
    expect(li.querySelector('form[action="/time-off/t1/decide"] button[name=approve][value="0"]')).not.toBeNull();
    expect(li.querySelector('details.timeoff-change form[action="/time-off/t1/change"]')).not.toBeNull();
    expect(li.querySelector('details.timeoff-change form[action="/time-off/t1/cancel-for"]')).not.toBeNull();
  });

  test('an approved request can be changed or cancelled, with what it holds filled in', () => {
    const f = item('t2').querySelector('form[action="/time-off/t2/change"]')!;
    expect(attr(f.querySelector('input[name=starts_on]'), 'value')).toBe('2026-12-07');
    expect(attr(f.querySelector('input[name=ends_on]'), 'value')).toBe('2026-12-09');
    expect(f.querySelector('select[name=kind] option[selected]')?.getAttribute('value')).toBe('vacation');
    expect((f.querySelector('input[name=half_day]') as HTMLInputElement).hasAttribute('checked')).toBe(true);
    expect(f.querySelector('textarea[name=comments]')?.textContent).toBe('flying out Sunday');
    expect(f.querySelector('input[name=note]')).not.toBeNull();
    const cancel = item('t2').querySelector('form[action="/time-off/t2/cancel-for"]')!;
    expect(attr(cancel, 'data-confirm')).toBe("Cancel Aria's time off request?");
    expect(cancel.querySelector('button')?.textContent).toContain('Cancel this request');
  });

  test('a cancelled request is finished: no actions, and it says who cancelled it and why', () => {
    const li = item('c1');
    expect(li.querySelector('form')).toBeNull();
    expect(words(li)).toContain('Cancelled by Yessid: the trip moved');
  });

  test('half a day is shown as taken: three days with a half day is 2.5', () => {
    expect(words(item('t2'))).toContain('2.5 working days (first day a half day)');
    expect(words(item('t1'))).toContain('3 working days');
  });

  test('the request form offers a half day', () => {
    expect(d.querySelector('form[action="/time-off"] input[name=half_day]')).not.toBeNull();
  });
});

describe('time off for the person', () => {
  const d = draw('timeoff', fixture('timeoff-own'));

  test('they see the new length and who cancelled theirs', () => {
    const t = text(d);
    expect(t).toContain('2.5 days (first day a half day)');
    expect(t).toContain('Cancelled by Yessid: the trip moved');
  });

  test('they cannot change a request, only cancel their own that is still open', () => {
    expect(d.querySelector('form[action$="/change"]')).toBeNull();
    expect(d.querySelector('form[action$="/cancel-for"]')).toBeNull();
    expect(d.querySelector('#r-t2 form[action="/time-off/t2/cancel"]')).not.toBeNull();
    expect(d.querySelector('#r-c1 form')).toBeNull();
  });
});
