import { describe, expect, test } from 'vitest';
import { draw, page, text } from './testing/draw';
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

// The Calendar page deletes an event a person made. Work that falls due, a time off Out and an entry from Google carry no delete.
describe('calendar delete', () => {
  const day = (entries: any[]) => ({ Date: '2026-11-24T00:00:00Z', InMonth: true, Today: false, Entries: entries });
  const cal = (entries: any[]) => page({ Month: '2026-11-01T00:00:00Z', PrevMonth: '2026-10', NextMonth: '2026-12', Days: [[day(entries)]], Departments: [] });
  test('an event gets a Delete button that posts to its delete route and asks first', () => {
    const d = draw('calendar', cal([{ ID: 'e1', Kind: 'event', Title: 'Standup' }]));
    const f = d.querySelector('form[action="/calendar/e1/delete"]');
    expect(f).not.toBeNull();
    expect(f!.getAttribute('data-confirm')).toBe('Delete this event?');
    expect(f!.querySelector('button')!.textContent).toBe('Delete');
  });
  test('work that falls due, a time off Out and a Google entry have none', () => {
    const d = draw('calendar', cal([
      { ID: 'w1', Kind: 'due', Title: 'Fix press', DocumentID: 'doc1' },
      { ID: 'o1', Kind: 'event', Title: 'Out: Ann' },
      { ID: 'g1', Kind: 'google', Title: 'From Google', Link: 'https://calendar.example/g1' },
    ]));
    expect(d.querySelector('[data-event-delete]')).toBeNull();
  });
});

describe('social, the rest of what the publishing app does', () => {
  test('a scheduled post can go back to a draft, a draft can be scheduled, a published one shows its numbers', () => {
    const d = draw('social', fixture('social-posts'));
    expect(d.querySelector('[data-post="p1"] form[action="/social/post/p1/status"] input[value="draft"]')).not.toBeNull();
    expect(d.querySelector('[data-post="p2"] form[action="/social/post/p2/status"] input[value="schedule"]')).not.toBeNull();
    expect(d.querySelector('[data-post="p3"] a[href="/social?post=p3"]')).not.toBeNull();
    expect(d.querySelector('[data-post="p3"] form[action$="/status"]')).toBeNull();
  });
  test('the activity tab lists what the app said, and the media tab lists the files and adds one from an address', () => {
    expect(text(draw('social', fixture('social-activity')))).toContain('published successfully');
    const m = draw('social', fixture('social-media'));
    expect(m.querySelector('[data-media] a[href="https://u.example/banner.png"]')).not.toBeNull();
    expect(m.querySelector('form[action="/social/media"] input[name=url]')).not.toBeNull();
  });
});
