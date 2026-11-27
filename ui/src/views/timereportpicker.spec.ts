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
    // nothing ticked: the buttons stay live and ask for everyone in the view (a dead button gave Yessid nothing)
    expect((panel!.querySelector('button[value=csv]') as HTMLButtonElement).disabled).toBe(false);
    expect((panel!.querySelector('button[value=xlsx]') as HTMLButtonElement).disabled).toBe(false);
    expect(panel!.querySelector('input[name=all][value="1"]')).not.toBeNull();
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
    const cells = Array.from(d.querySelectorAll('tbody tr:not(.noterow)')).map((r) => r.lastElementChild?.textContent?.trim());
    expect(cells).toEqual(['Approved', 'Pending', 'Pending']);
  });

  // Yessid, 2026-11-27, on Carlos Torrijos's page: a long note made its row a huge block and pushed the Note column off the
  // right edge. A note now sits on a row of its own the width of the table; a short one is shown whole, a very long one is
  // cut with a "more" that opens it, and the full text is always in the page.
  test('a note has a row of its own under its entry, whole when short and cut with a more when long', () => {
    const d = draw('timereport', fixture('timereport-person-approval'));
    expect(d.querySelectorAll('thead th').length, 'the Note column is gone: notes have their own row').toBe(8);
    const rows = Array.from(d.querySelectorAll('tbody tr'));
    const notes = rows.filter((r) => r.classList.contains('noterow'));
    expect(notes.length).toBe(2);
    expect(notes[0].querySelector('td')?.getAttribute('colspan')).toBe('8');
    expect(notes[0].querySelector('details.note .short')?.textContent).toContain('more');
    expect(notes[0].querySelector('details.note .full')?.textContent).toContain('first power-up check with the customer');
    expect(notes[1].textContent?.trim()).toBe('short note');
    expect(notes[1].querySelector('details')).toBeNull();
    // the entry above a note carries no bottom rule, so the two read as one
    expect(rows[1].classList.contains('hasnote')).toBe(true);
  });

  test('the entries table shares the width of its box instead of running off it', () => {
    const d = draw('timereport', fixture('timereport-person-approval'));
    const t = d.querySelector('table.list');
    expect(t?.classList.contains('reportrows')).toBe(true);
    expect(t?.querySelectorAll('colgroup col').length).toBe(8);
  });

  // Yessid's filter row ran off the page and cut Team to "T": each filter is a labelled box in a grid that wraps to the width.
  test('every filter is a labelled box in the wrapping filter grid', () => {
    const d = draw('timereport', fixture('timereport-person-approval'));
    const form = d.querySelector('form.reportfilters');
    expect(form, 'the filter bar is not the wrapping grid').not.toBeNull();
    for (const id of ['from', 'to', 'f-approval', 'f-team', 'f-space', 'f-tag']) {
      const control = form!.querySelector('#' + id);
      expect(control, id).not.toBeNull();
      expect(control!.parentElement?.classList.contains('fld'), `${id} is not in a labelled box`).toBe(true);
      expect(form!.querySelector(`label[for=${id}]`), `${id} lost its label`).not.toBeNull();
    }
    expect(form!.querySelector('.fld-go button')?.textContent).toBe('Show');
  });

  test('the three-level case reads Space, Customer/Project, Phase and Task on the person page', () => {
    const d = draw('timereport', fixture('timereport-person-approval'));
    const first = Array.from(d.querySelectorAll('tbody tr')[0].querySelectorAll('td')).map((c) => c.textContent?.trim());
    expect(first.slice(2, 6)).toEqual(['White Team Service', 'MCSI', 'MCS1002 Beachwood Ohio', 'Panel Integration']);
  });
});
