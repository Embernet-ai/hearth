import { describe, expect, test } from 'vitest';
import { draw, text } from './testing/draw';
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
