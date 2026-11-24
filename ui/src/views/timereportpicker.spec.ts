import { describe, expect, test } from 'vitest';
import { draw, text } from './testing/draw';
import { fixture } from './testing/fixture';

describe('the time report details export', () => {
  test('offers every person even when nobody logged time in the range, and the buttons wake up when somebody is ticked', () => {
    const d = draw('timereport', fixture('timereport-nobody-logged'));
    expect(text(d)).toContain('Nothing logged in this range');
    const panel = d.querySelector('form.detail-export');
    expect(panel, 'the Export details panel is missing when nobody logged time').not.toBeNull();
    expect(panel!.querySelectorAll('input[name=people]').length).toBe(2);
    expect(text(d)).toContain('Ann Lee');
    expect(text(d)).toContain('Select all (2)');
    // nothing ticked yet: the download buttons are off until somebody is chosen
    expect((panel!.querySelector('button[value=csv]') as HTMLButtonElement).disabled).toBe(true);
  });
  test('says why an export came back empty', () => {
    const d = draw('timereport', fixture('timereport-nobody-logged'));
    expect(d.querySelector('[data-flash]')?.textContent).toContain('Nothing to export');
  });
});
