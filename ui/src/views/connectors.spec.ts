import { describe, expect, test } from 'vitest';
import { draw, text } from './testing/draw';
import { fixture } from './testing/fixture';

// The Connectors page puts the controls that decide what is allowed (the tenant, the switches, everybody's status) in
// front of an administrator only, and everybody's own connection in front of everybody. Settings links to it only when
// the connectors exist on this installation.
describe('connectors', () => {
  test('an administrator sees the tenant, the switches and who is connected', () => {
    const d = draw('connectors', fixture('connectors-admin'));
    expect(d.querySelector('form[action="/settings/connectors/tenant"]')).not.toBeNull();
    expect(d.querySelectorAll('form[action="/settings/connectors/toggle"]').length).toBeGreaterThan(0);
    expect(text(d)).toContain('Who is connected');
    expect(d.querySelector('form[action="/settings/connectors/remove"]')).not.toBeNull();
    expect(text(d)).toContain('3 connected');
  });

  test('a member sees their own connection and none of the controls', () => {
    const d = draw('connectors', fixture('connectors-member'));
    expect(d.querySelector('form[action="/settings/connectors/tenant"]')).toBeNull();
    expect(d.querySelector('form[action="/settings/connectors/toggle"]')).toBeNull();
    expect(d.querySelector('form[action="/settings/connectors/remove"]')).toBeNull();
    expect(text(d)).not.toContain('Who is connected');
    expect(text(d)).toContain('dana@contoso.test');
    expect(d.querySelector('form[action="/settings/connectors/sync"]')).not.toBeNull();
    expect(d.querySelector('form[action="/settings/connectors/disconnect"]')).not.toBeNull();
  });

  test('a provider the installation has not set up says so and offers nothing', () => {
    const d = draw('connectors', fixture('connectors-admin'));
    const google = d.querySelector('[data-provider="google"]')!;
    expect(google.textContent).toContain('no Google Workspace client set up');
    expect(google.querySelector('form')).toBeNull();
  });

  test('only a connector that is on can be connected for, and chat asks for no permission', () => {
    const d = draw('connectors', fixture('connectors-member'));
    const boxes = [...d.querySelectorAll('form[action="/integrations/microsoft/connect"] input[name="connector"]')].map((i) => (i as HTMLInputElement).value);
    expect(boxes).toEqual([]); // the calendar is already covered and contacts is off, so nothing is offered
  });

  test('a calendar can be sent to a department the person manages', () => {
    const d = draw('connectors', fixture('connectors-member'));
    const sel = d.querySelector('form[action="/settings/connectors/calendar"] select[name="space"]')!;
    expect(sel.textContent).toContain('Plant 3');
  });

  test('settings links to the page only when the connectors exist', () => {
    expect(draw('settings', fixture('settings-connectors')).querySelector('a[href="/settings/connectors"]')).not.toBeNull();
    expect(draw('settings', fixture('settings-nobody')).querySelector('a[href="/settings/connectors"]')).toBeNull();
  });
});

describe('cloud files', () => {
  test('browsing offers link and import for a file and opens a folder', () => {
    const d = draw('cloud', fixture('cloud-browse'));
    expect(d.querySelector('form[action="/cloud/link"]')).not.toBeNull();
    expect(d.querySelector('button[formaction="/cloud/import"]')).not.toBeNull();
    expect(d.querySelector('a[href*="folder=fo"]')).not.toBeNull();
    expect(text(d)).toContain('Quote 118');
  });

  test('with nothing connected it points at the Connectors page', () => {
    const d = draw('cloud', fixture('cloud-none'));
    expect(d.querySelector('a[href="/settings/connectors"]')).not.toBeNull();
    expect(d.querySelector('form[action="/cloud/link"]')).toBeNull();
  });
});

describe('cloud files, writing', () => {
  test('each file offers rename, move, replace and delete, and the folder takes an upload and a new folder', () => {
    const d = draw('cloud', fixture('cloud-browse'));
    expect(d.querySelector('form[action="/cloud/upload"][enctype="multipart/form-data"]')).not.toBeNull();
    expect(d.querySelector('form[action="/cloud/mkdir"]')).not.toBeNull();
    for (const a of ['rename', 'move', 'replace', 'delete']) {
      expect(d.querySelector(`form[action="/cloud/${a}"]`), a).not.toBeNull();
    }
    // A folder cannot have its content replaced.
    const folder = d.querySelector('[data-item="fo"]')!;
    expect(folder.querySelector('form[action="/cloud/replace"]')).toBeNull();
  });

  test('saving a Hearth attachment shows what is being saved and where it goes', () => {
    const d = draw('cloud', fixture('cloud-save'));
    const f = d.querySelector('form[data-save]')!;
    expect(f.textContent).toContain('spec.txt');
    expect((f.querySelector('input[name="savefile"]') as HTMLInputElement).value).toBe('file1');
    // A file can move into the folder; the folder itself is not offered as its own destination.
    const moveFile = d.querySelector('[data-item="fi"] select[name="to"]')!;
    expect(moveFile.textContent).toContain('Quotes');
    expect(d.querySelector('[data-item="fo"] select[name="to"]')!.textContent).not.toContain('Quotes');
  });
});

describe('mailbox', () => {
  test('the list shows each message and a compose form', () => {
    const d = draw('mailbox', fixture('mailbox-list'));
    expect(d.querySelector('[data-message="m1"] a[href*="id=m1"]')).not.toBeNull();
    expect(d.querySelector('form[action="/mailbox/send"]')).not.toBeNull();
    expect(text(d)).toContain('Older messages');
  });

  test('an open message shows the whole body and every action', () => {
    const d = draw('mailbox', fixture('mailbox-open'));
    expect(text(d)).toContain('Hi Dana, the quote.');
    expect(text(d)).toContain('quote-118.pdf');
    for (const a of ['reply', 'forward', 'archive', 'label', 'read', 'delete']) {
      expect(d.querySelector(`form[action="/mailbox/${a}"]`), a).not.toBeNull();
    }
    expect(d.querySelector('form[action="/mailbox/reply"] input[name="all"]')).not.toBeNull();
  });

  test('with no mail connected it points at the Connectors page', () => {
    const d = draw('mailbox', fixture('mailbox-none'));
    expect(d.querySelector('a[href="/settings/connectors"]')).not.toBeNull();
    expect(d.querySelector('form[action="/mailbox/send"]')).toBeNull();
  });
});
