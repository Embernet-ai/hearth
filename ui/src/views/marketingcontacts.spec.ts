import { describe, expect, test } from 'vitest';
import { draw, text } from './testing/draw';
import { fixture } from './testing/fixture';

describe('the Marketing contacts tab', () => {
  test('lists the people with their tags and do not email marks, and offers a new one', () => {
    const d = draw('marketing', fixture('marketing-contacts'));
    expect(d.querySelectorAll('[data-contacts] tbody tr').length).toBe(2);
    expect(text(d)).toContain('Ann Lee');
    expect(text(d)).toContain('do not email');
    expect(d.querySelector('a[href="/marketing?tab=contacts&new=1"]')).not.toBeNull();
    expect(d.querySelector('a[href="/marketing?tab=contacts"]')).not.toBeNull();
  });
  test('one contact can be changed, joined to a segment, sent an email, marked and deleted', () => {
    const d = draw('marketing', fixture('marketing-contact'));
    for (const a of ['/marketing/contact', '/marketing/contact/7/segment', '/marketing/contact/7/send', '/marketing/contact/7/dnc', '/marketing/contact/7/delete']) {
      expect(d.querySelector(`form[action="${a}"]`), a).not.toBeNull();
    }
  });
  test('an email can be tried on an address before it goes out', () => {
    const d = draw('marketing', fixture('marketing-email-edit'));
    expect(d.querySelector('form[data-test-send] input[name=to]')).not.toBeNull();
  });
});
