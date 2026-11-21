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
