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

describe('the time report filters', () => {
  test('offers approval, team and tag, shows what is chosen, and carries it to the downloads and the details export', () => {
    const d = draw('timereport', fixture('timereport-filtered'));
    const approval = d.querySelector('select[name=approval]') as HTMLSelectElement;
    expect(approval, 'the approval filter is missing').not.toBeNull();
    expect(Array.from(approval.options).map((o) => o.value)).toEqual(['', 'approved', 'pending', 'rejected']);
    const chosen = (sel: Element | null) => sel?.querySelector('option[selected]')?.getAttribute('value');
    expect(chosen(approval)).toBe('pending');
    expect(chosen(d.querySelector('select[name=team]'))).toBe('t1');
    expect(chosen(d.querySelector('select[name=tag]'))).toBe('Billable');
    const csv = Array.from(d.querySelectorAll('a.chip')).find((a) => a.textContent?.includes('CSV')) as HTMLAnchorElement;
    expect(csv.getAttribute('href')).toContain('&approval=pending&team=t1&tag=Billable');
    const panel = d.querySelector('form.detail-export')!;
    expect((panel.querySelector('input[name=approval]') as HTMLInputElement).value).toBe('pending');
    expect((panel.querySelector('input[name=team]') as HTMLInputElement).value).toBe('t1');
    expect(text(d)).toContain('approved, pending or rejected');
  });
  test('shows each entry\'s approval on a person\'s report', () => {
    const d = draw('timereport', fixture('timereport-person-approval'));
    const cells = Array.from(d.querySelectorAll('tbody tr')).map((r) => r.lastElementChild?.textContent?.trim());
    expect(cells).toEqual(['Approved', 'Pending']);
  });
});
