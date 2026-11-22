import { describe, expect, test } from 'vitest';
import { draw, text } from './testing/draw';
import { fixture } from './testing/fixture';

// Marketing, Forms, Social and Tools each say plainly when they are not set up, and otherwise show the app's records
// with the controls to change them.
describe('marketing', () => {
  test('says it is not connected and offers nothing when the app is not set up', () => {
    const d = draw('marketing', fixture('marketing-off'));
    expect(d.querySelector('[data-not-set-up]')).not.toBeNull();
    expect(d.querySelector('form')).toBeNull();
  });
  test('lists emails with publish, send and delete', () => {
    const d = draw('marketing', fixture('marketing-emails'));
    expect(d.querySelectorAll('[data-email]').length).toBe(2);
    expect(d.querySelector('form[action="/marketing/email/1/send"]')).not.toBeNull();
    expect(d.querySelector('form[action="/marketing/email/2/send"]')).toBeNull();
    expect(d.querySelector('form[action="/marketing/email/1/delete"]')).not.toBeNull();
  });
  test('edits an email, a campaign and a segment', () => {
    expect(draw('marketing', fixture('marketing-email-edit')).querySelector('form[action="/marketing/email"] input[name="id"]')).not.toBeNull();
    expect(draw('marketing', fixture('marketing-campaign-new')).querySelector('form[action="/marketing/campaign"] select[name="email"]')).not.toBeNull();
    expect(draw('marketing', fixture('marketing-campaigns')).querySelector('form[action="/marketing/campaign/5/publish"]')).not.toBeNull();
    expect(draw('marketing', fixture('marketing-segments')).querySelector('form[action="/marketing/segment/3/delete"]')).not.toBeNull();
  });
});

describe('forms', () => {
  test('says it is not connected when the app is not set up', () => {
    expect(draw('forms', fixture('forms-off')).querySelector('[data-not-set-up]')).not.toBeNull();
  });
  test('lists forms and shows a scheduled one as scheduled', () => {
    const d = draw('forms', fixture('forms-list'));
    expect(d.querySelectorAll('[data-form]').length).toBe(2);
    expect(text(d)).toContain('Scheduled');
    expect(text(d)).toContain('2026-12-01');
  });
  test('edits a form: status, schedule, answers and delete', () => {
    const d = draw('forms', fixture('forms-edit'));
    expect(d.querySelector('form[action="/forms/f1/status"]')).not.toBeNull();
    expect(d.querySelector('form[action="/forms/f1/schedule"] input[name="publish_on"]')).not.toBeNull();
    expect(d.querySelector('form[action="/forms/f1/delete"]')).not.toBeNull();
  });
  test('shows the answers under the question they answer', () => {
    const d = draw('forms', fixture('forms-results'));
    expect(text(d)).toContain('Anything else?');
    expect(text(d)).toContain('Great');
  });
});

describe('social', () => {
  test('says it is not connected, and says when no channel is', () => {
    expect(draw('social', fixture('social-off')).querySelector('[data-not-set-up]')).not.toBeNull();
    expect(draw('social', fixture('social-none')).querySelector('[data-no-channels]')).not.toBeNull();
  });
  test('composes to a channel and lists the posts', () => {
    const d = draw('social', fixture('social-posts'));
    expect(d.querySelector('form[action="/social/post"] input[name="channel"]')).not.toBeNull();
    expect(d.querySelector('form[action="/social/post/p1/delete"]')).not.toBeNull();
    expect(text(d)).toContain('Scheduled');
  });
});

describe('tools', () => {
  test('a card for each tool, opening in a new tab', () => {
    const d = draw('tools', fixture('tools-list'));
    expect(d.querySelectorAll('[data-tool]').length).toBe(2);
    expect(d.querySelector('[data-tool="Ops"] a')!.getAttribute('href')).toBe('https://ops.example');
    expect(text(d)).toContain('Sign in with Vord');
  });
  test('says so when none are set up', () => {
    expect(draw('tools', fixture('tools-none')).querySelector('[data-not-set-up]')).not.toBeNull();
  });
});
