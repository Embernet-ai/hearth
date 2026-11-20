import { describe, expect, test } from 'vitest';
import { draw, text } from './testing/draw';
import { fixture } from './testing/fixture';

const forms = (d: Document, prefix: string) => [...d.querySelectorAll('form')].filter((f) => (f.getAttribute('action') ?? '').startsWith(prefix));

describe('a person\'s page', () => {
  // Everything about one person on one page, every change coming back to it, and the Finance department kept visibly apart
  // from the Finance grant: the mix-up that made somebody's billing access look granted when it was not.
  test('holds everything about them, and every change comes back to it', () => {
    const d = draw('person', fixture('person-everything'));
    const t = text(d);
    for (const want of ['Works in', 'Hours approved by', 'Manages', 'Can see work in', 'Can open', 'Account', 'included', 'Save access']) {
      expect(t, `person page is missing ${want}`).toContain(want);
    }
    expect(d.querySelector('[title="Take them out of Engineering"]')).not.toBeNull();
    expect(d.querySelector('[title="Stop managing Red Team"]')).not.toBeNull();
    expect(d.querySelector('input[name=space][value=d2][data-depth="0"][checked]')).not.toBeNull();
    expect(d.querySelector('option[value=p2][selected]')).not.toBeNull();
    const about = forms(d, '/people/p1/');
    const back = about.filter((f) => f.querySelector('input[name=back][value="/people/p1"]'));
    expect(about.length).toBeGreaterThan(0);
    expect(back.length, 'forms that post about this person must all come back to their page').toBe(about.length);
  });

  // Suspend is the owner's and the super admins'. Anybody else is not shown a button that would only answer 403.
  test('does not offer to suspend to somebody who cannot, or to anybody on the owner\'s own account', () => {
    expect(forms(draw('person', fixture('person-not-admin')), '/people/p1/status')).toHaveLength(0);
    expect(forms(draw('person', fixture('person-of-the-owner')), '/people/p1/status')).toHaveLength(0);
    expect(forms(draw('person', fixture('person-everything')), '/people/p1/status')).toHaveLength(1);
  });

  // Somebody who is not an admin (HR holding People, say) reads the same facts about a person and gets no control that moves them.
  test('is read only below admin', () => {
    const d = draw('person', fixture('person-read-only'));
    const t = text(d);
    for (const want of ['Engineering', 'Yara', 'Only an admin can change who sees what']) expect(t).toContain(want);
    expect(d.querySelector('input[value=d1][data-depth="0"][checked][disabled]')).not.toBeNull();
    for (const action of ['departments', 'approver', 'manages', 'access', 'grant-space']) {
      expect(forms(d, `/people/p1/${action}`), `a non-admin was given the ${action} form`).toHaveLength(0);
    }
    expect(t).not.toContain('Can David see Engineering');
    expect(t).not.toContain('Save access');
  });

  // Right after somebody is added to a department, their page asks whether they can see its work.
  test('asks about access to a new department', () => {
    const d = draw('person', fixture('person-asks-access'));
    const t = text(d);
    for (const want of ['Can David see Engineering', 'Yes, all of Engineering', 'Only some parts']) expect(t).toContain(want);
    expect(d.querySelector('input[name=space][value=d1]')).not.toBeNull();
    expect(d.querySelector('input[type=checkbox][name=space][value=d3]')).not.toBeNull();
  });

  test('the people list links to each person', () => {
    const d = draw('people', fixture('people'));
    const t = text(d);
    expect(d.querySelector('a[href="/people/p1"]')).not.toBeNull();
    for (const want of ['How this page works', 'nothing yet']) expect(t).toContain(want);
    expect((d.querySelector('input[value=dav]') as HTMLInputElement | null)).not.toBeNull();
  });
});
