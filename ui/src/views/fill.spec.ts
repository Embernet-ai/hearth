import { describe, expect, test } from 'vitest';
import { draw, text } from './testing/draw';
import { fixture } from './testing/fixture';

// The screens that fill the width were re-laid out. Nothing a person could do or read there may be gone: every section heading,
// every link and every control the old layout had is still drawn, for the same people. (The widths are checked in a browser by
// ui/layout/fill.mjs; this checks what is on the page.)
const links = (d: Document) => [...d.querySelectorAll('main a[href], a[href]')].map((a) => a.getAttribute('href'));
const heads = (d: Document) => [...d.querySelectorAll('h1,h2,h3')].map((h) => h.textContent?.trim());

describe('screens that fill the width keep everything', () => {
  test('settings: every group and link for an owner', () => {
    const d = draw('settings', fixture('settings-everything'));
    for (const h of ['Settings', 'Personal', 'Account', 'Notifications', 'Tutorial', 'Administration', 'People', 'Automation', 'Oversight', 'Billing']) expect(heads(d)).toContain(h);
    for (const l of ['/account', '/departments/request', '/inbox#notifications', '/settings/tutorial', '/people', '/teams', '/rules', '/agents', '/settings/mail', '/integrations', '/audit', '/ops/jobs', '/reports/time', '/time/imported'])
      expect(links(d), l).toContain(l);
  });
  test('inbox: the list, its controls and the preferences', () => {
    const d = draw('inbox', fixture('inbox-full'));
    expect(d.querySelectorAll('li[data-notification]').length).toBe(4);
    expect(d.querySelectorAll('li[data-notification] form[action="/inbox/read"]').length).toBe(3); // the read one has none
    expect(d.querySelector('form[action="/inbox/read"] button')?.textContent).toContain('Mark all read');
    for (const n of ['email', 'digest_at', 'quiet_from', 'quiet_to', 'timezone']) expect(d.querySelector(`[name="${n}"]`), n).not.toBeNull();
    expect(d.querySelector('#notifications')).not.toBeNull();
    expect(text(d)).toContain('Everything');
  });
  test('outbox: the three sections', () => {
    const d = draw('outbox', fixture('outbox-full'));
    for (const h of ['On this phone', 'Waiting on you', 'Lately']) expect(heads(d)).toContain(h);
    expect(d.querySelector('[data-queue]')).not.toBeNull();
    expect(d.querySelector('[data-queue-empty]')).not.toBeNull();
    expect(d.querySelectorAll('[data-undecided] li').length).toBe(2);
    expect(d.querySelectorAll('[data-recent] li').length).toBe(3);
  });
  test('unassigned: the tabs, the search and a claim and hand-off for each row', () => {
    const d = draw('unassigned', fixture('unassigned-full'));
    expect(d.querySelectorAll('tr[data-record]').length).toBe(3);
    expect(d.querySelectorAll('tr[data-record] button[value="me"]').length).toBe(3);
    expect(d.querySelectorAll('tr[data-record] select[name="owner"]').length).toBe(3);
    expect(d.querySelector('input[name="q"]')).not.toBeNull();
    expect(text(d)).toMatch(/Companies \(3\).*People \(7\).*Deals \(2\)/);
  });
  test('channels: every channel with join or leave, and the new channel form', () => {
    const d = draw('channels', fixture('channels-full'));
    expect(d.querySelectorAll('tbody tr').length).toBe(4);
    expect(d.querySelectorAll('form[action$="/leave"]').length).toBe(3);
    expect(d.querySelectorAll('form[action$="/join"]').length).toBe(1);
    expect(d.querySelector('form[action="/channels"] input[name="name"]')).not.toBeNull();
    expect(d.querySelector('form[action="/channels"] select[name="visibility"]')).not.toBeNull();
    // the five headings stay on the page for every width; that they are drawn in words at 1280, 1440, 1920, 2560 and 390 is ui/layout/fill.mjs
    expect([...d.querySelectorAll('thead th')].map((t) => t.textContent?.trim())).toEqual(['Channel', 'Topic', 'People', 'Grants', 'Unread', '']);
    expect([...d.querySelectorAll('tbody tr')].every((r) => r.children.length === 6)).toBe(true); // every field is there, so every one gets its label
  });
  test('contacts: the old columns, the new ones, the add form', () => {
    const d = draw('contacts', fixture('contacts-full'));
    const th = [...d.querySelectorAll('thead th')].map((t) => t.textContent?.trim());
    for (const c of ['Name', 'Company', 'Title', 'Email', 'Phone', 'Owner']) expect(th).toContain(c);
    expect(d.querySelectorAll('tr[data-contact]').length).toBe(3);
    expect(d.querySelector('form[action="/contacts"] input[name="name"]')).not.toBeNull();
    expect(links(d)).toContain('/crm/import');
  });
  test('dashboard: the report card, the week, the numbers and the lists', () => {
    const d = draw('dashboard', fixture('dashboard-full'));
    for (const h of ['My daily report', 'My numbers', 'Assigned to me', 'Things you follow']) expect(heads(d).join('|')).toContain(h);
    expect(d.querySelectorAll('.weekday').length).toBe(7);
    expect(d.querySelectorAll('.numbers:not(.recap) a.stat').length).toBe(14);
    // the team's parts: email marketing and social with four numbers each, and forms that did not answer, saying so
    expect(heads(d).join('|')).toContain('Marketing and social');
    expect(d.querySelectorAll('.recap a.stat').length).toBe(8);
    expect(d.querySelector('.recap .g-forms .groupnote')?.textContent).toContain('did not answer');
    expect(d.querySelector('form[action="/time"]')).not.toBeNull();
    expect(d.querySelector('form[action="/time/start"]')).not.toBeNull();
  });
});

describe('the import file chooser', () => {
  for (const [tpl, fx] of [['crmimport', 'crmimport'], ['import', 'crmimport']] as const) {
    test(`${tpl}: a real file input in the form, named file, required, with the picker around it`, () => {
      const d = draw(tpl, fixture(fx));
      const input = d.querySelector('form input[type="file"]') as HTMLInputElement;
      expect(input.getAttribute('name')).toBe('file');
      expect(input.hasAttribute('required')).toBe(true);
      expect(input.getAttribute('accept')).toContain('.csv');
      expect(d.querySelector('label.filepick-button[for="file"]')?.textContent).toBe('Choose file');
      expect(d.querySelector('[data-filepick]')).not.toBeNull();
      expect(text(d)).toContain('drop a');
    });
  }
});
