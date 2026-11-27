import { describe, expect, test } from 'vitest';
import { draw, page, text } from './testing/draw';
import { fixture } from './testing/fixture';

describe('spaces', () => {
  // The Spaces page shows edit controls and the "+" only for spaces the reader may manage.
  test('gates edit to manageable spaces', () => {
    const d = draw('spaces', fixture('spaces'));
    expect(d.querySelector('form[action="/spaces/mine/rename"]')).not.toBeNull();
    expect(d.querySelector('a[href="/spaces?parent=mine"]')).not.toBeNull();
    expect(d.querySelector('form[action="/spaces/theirs/rename"]')).toBeNull();
    expect(d.querySelector('a[href="/spaces?parent=theirs"]')).toBeNull();
  });
});

describe('daily reports', () => {
  // The filing form has to ask for all three things at once, with a camera input that opens the back camera, and post as multipart.
  test('the form asks for text, photos and a comment', () => {
    const d = draw('dailyreport', fixture('dailyreport-form'));
    const form = d.querySelector('form[action="/reports/daily"]');
    expect(form?.getAttribute('enctype')).toBe('multipart/form-data');
    expect(d.querySelector('[name=body]')).not.toBeNull();
    expect(d.querySelector('input[name=camera][capture=environment]')).not.toBeNull();
    expect(d.querySelector('input[name=photos][multiple]')).not.toBeNull();
    for (const n of ['files', 'comment', 'space']) expect(d.querySelector(`[name=${n}]`), n).not.toBeNull();
    expect(d.querySelector('option[value=sp2][selected]')).not.toBeNull();
    expect(text(d)).toContain('kept text');
    expect(text(d)).toContain('Write what you did today');
  });

  test('the roll-up shows reports, photos, comments and the PDF link', () => {
    const d = draw('dailyreports', fixture('dailyreports-rollup'));
    const hrefs = [...d.querySelectorAll('a')].map((a) => a.getAttribute('href'));
    const id = '11111111-1111-1111-1111-111111111111';
    for (const want of ['/reports/daily.pdf?from=2026-10-20&to=2026-10-26', `/documents/${id}/report.pdf`, `/documents/${id}/files/f1`]) {
      expect(hrefs, want).toContain(want);
    }
    const t = text(d);
    for (const want of ['Done: wiring', 'panel.jpg', 'Yara', 'File today']) expect(t).toContain(want);
  });

  test('having filed, the button turns into a link to the report', () => {
    const t = text(draw('dailyreports', fixture('dailyreports-filed')));
    expect(t).toContain('Open my report for today');
    expect(t).not.toContain("File today's report");
  });

  test('the dashboard card says whether today\'s report is filed', () => {
    const notYet = draw('dashboard', fixture('dashboard-not-filed'));
    expect(notYet.querySelector('a[href="/reports/daily/new"]')).not.toBeNull();
    expect(text(notYet)).toContain('Not filed yet');
    const filed = draw('dashboard', fixture('dashboard-filed'));
    expect(filed.querySelector('a[href="/documents/rid/report.pdf"]')).not.toBeNull();
    expect(text(filed)).not.toContain('Not filed yet');
  });

  test('the service history page offers both downloads', () => {
    const d = draw('servicehistory', fixture('servicehistory'));
    const hrefs = [...d.querySelectorAll('a')].map((a) => a.getAttribute('href'));
    expect(hrefs).toContain('/reports/service.pdf?space=sp1&from=2025-01-01');
    expect(hrefs).toContain('/reports/service.csv?space=sp1');
    const t = text(d);
    for (const want of ['3 records', 'Drive fault', 'Line 3']) expect(t).toContain(want);
    expect(d.querySelector('option[value=sp1][selected]')).not.toBeNull();
  });
});

// The relay example on the mail page names the endpoint to post to.
test('the mail page names the inbound endpoint', () => {
  expect(text(draw('mail', fixture('mail')))).toContain('@- https://hearth.example.com/inbound/mail"');
});

// Pages shown before anybody is signed in are drawn without the rail and top bar (components/App.svelte), by their own views.
describe('before sign-in', () => {
  const anon = { Identity: null };
  test('first-run setup asks for the organisation, a name, an email and a password, and says what went wrong', () => {
    const d = draw('setup', fixture('setup', anon));
    const form = d.querySelector('form[action="/setup"]');
    expect(form).not.toBeNull();
    for (const n of ['org', 'name', 'email', 'password']) expect(form?.querySelector(`[name=${n}]`), n).not.toBeNull();
    expect(text(d)).toContain('password is too short');
  });

  test('choosing a password from a link posts the token and names the account', () => {
    const d = draw('setpassword', fixture('setpassword', anon));
    expect(d.querySelector('form[action="/set-password"] input[name=t][value=tok]')).not.toBeNull();
    expect(text(d)).toContain('ana@example.com');
    expect(text(draw("setpassword", fixture("setpassword-done", anon)))).toContain("Your password is set");
  });
});

describe('time report', () => {
  const totals = [
    { PrincipalID: 'p1', PersonName: 'Ann Lee', Minutes: 150 },
    { PrincipalID: 'p2', PersonName: 'Bo Diaz', Minutes: 480 },
  ];
  const range = { ReportFrom: '2026-11-01', ReportTo: '2026-11-30', ReportTotal: 630, Totals: totals, ReportWhoOptions: totals };

  // The totals table and its two downloads are what the page was, and stay.
  test('the totals view keeps its table and its two downloads', () => {
    const d = draw('timereport', page(range));
    const hrefs = [...d.querySelectorAll('a')].map((a) => a.getAttribute('href'));
    expect(hrefs).toContain('/reports/time?from=2026-11-01&to=2026-11-30&format=csv');
    expect(hrefs).toContain('/reports/time?from=2026-11-01&to=2026-11-30&format=xlsx');
    expect(hrefs).toContain('/reports/time?person=p1&from=2026-11-01&to=2026-11-30');
    expect(text(d)).toContain('2 people');
  });

  // Details follow the People selector in the filter bar: nobody picked is everyone in view, and a link per format.
  test('the details export follows the people picked in the filter bar, and offers both formats', () => {
    const d = draw('timereport', page(range));
    expect(d.querySelector('form.detail-export'), 'the tick list at the bottom is gone').toBeNull();
    const bar = d.querySelector('form.reportfilters');
    expect(bar?.getAttribute('action')).toBe('/reports/time');
    expect(bar?.getAttribute('method')).toBe('get');
    for (const id of ['p1', 'p2']) expect(bar?.querySelector(`details.whopick input[name=people][value=${id}]`), id).not.toBeNull();
    const links = [...d.querySelectorAll('a[data-detail]')] as HTMLAnchorElement[];
    expect(links.map((a) => a.getAttribute('data-detail'))).toEqual(['csv', 'xlsx']);
    for (const a of links) {
      const q = new URLSearchParams(a.getAttribute('href')!.split('?')[1]);
      expect(q.get('detail')).toBe('1');
      expect(q.get('from')).toBe('2026-11-01');
      expect(q.get('to')).toBe('2026-11-30');
      expect(q.get('all'), 'nobody picked asks for everyone in the view').toBe('1');
    }
    expect(text(d)).toContain('Everyone in this view (2)');
  });

  test('one person drilled into has no details export and keeps its own downloads', () => {
    const d = draw('timereport', page({ ...range, Totals: null, ReportPerson: 'p1', ReportPersonName: 'Ann Lee', Report: [], ReportTotal: 150 }));
    expect(d.querySelector('.detail-export')).toBeNull();
    const hrefs = [...d.querySelectorAll('a')].map((a) => a.getAttribute('href'));
    expect(hrefs).toContain('/reports/time?person=p1&from=2026-11-01&to=2026-11-30&format=csv');
  });
});
