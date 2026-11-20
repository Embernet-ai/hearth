import { describe, expect, test } from 'vitest';
import { draw, text } from './testing/draw';
import { fixture } from './testing/fixture';

describe('approvals', () => {
  // Nobody picked: the totals, the departments with "No department" last, and a table of people to open, not everybody's entries.
  test('overview', () => {
    const d = draw('approvals', fixture('approvals-overview'));
    const t = text(d);
    expect(t).toContain('4 entries waiting');
    expect(t).toContain('3 people');
    const opts = [...d.querySelectorAll('option')].map((o) => o.getAttribute('value'));
    for (const v of ['eng', 'white', 'none']) expect(opts).toContain(v);
    expect(opts.indexOf('none'), 'No department comes after the real departments').toBeGreaterThan(opts.indexOf('white'));
    expect(d.querySelector('input[name=entry][value=e1]'), 'the overview lists nobody\'s entries').toBeNull();
  });

  test('a department narrows the employees to the people in it', () => {
    const d = draw('approvals', fixture('approvals-white'));
    const people = [...d.querySelectorAll('option')].map((o) => o.getAttribute('value'));
    expect(people).toContain('p1');
    expect(people).not.toContain('p2');
  });

  // A person: only their entries, approve-all carries exactly their ids, and every form returns to this department and person.
  test('a person', () => {
    const d = draw('approvals', fixture('approvals-gabriel'));
    expect(text(d)).toContain('Approve all 2');
    expect(d.querySelector('input[type=hidden][name=entry][value=e3]')).not.toBeNull();
    // The tick boxes join the bulk form through its id (the form attribute, set when the page is drawn in the browser).
    expect(d.querySelector('input[type=checkbox][name=entry][value=e1]')).not.toBeNull();
    expect(d.querySelector('#bulk')).not.toBeNull();
    expect(d.querySelector('input[name=back][value="/time/approvals?dept=white&person=p1"]')).not.toBeNull();
    expect(d.querySelector('input[name=entry][value=e2]'), 'Aria\'s entry showed on Gabriel\'s view').toBeNull();
  });

  // A department request renders as the decision it is: every department as a box, the asked ones ticked, and an Approve that
  // asks for confirmation with the ticked list, apart from Say no.
  test('a department request is a decision', () => {
    const d = draw('approvals', fixture('approvals-request'));
    expect(d.querySelector('form[action="/departments/requests/r1/decide"]')).not.toBeNull();
    expect(d.querySelector('input[value=w1][data-label="White Team"][checked]')).not.toBeNull();
    const e1 = d.querySelector('input[value=e1][data-label=Engineering]');
    expect(e1).not.toBeNull();
    expect(e1?.hasAttribute('checked')).toBe(false);
    expect(d.querySelector('[data-confirm-list=space]')).not.toBeNull();
    expect(d.querySelector('button[value=deny], input[value=deny]')).not.toBeNull();
    const t = text(d);
    expect(t).toContain('Constanza');
    expect(t).toContain('no department');
    expect(t).not.toContain('Nothing is waiting on you');
  });

  test('the person\'s own page ticks what they asked for', () => {
    const d = draw('deptrequest', fixture('deptrequest-pending'));
    expect(d.querySelector('input[value=w1][checked]')).not.toBeNull();
    expect(d.querySelector('input[value=e1][checked]')).toBeNull();
    expect(text(d)).toContain('Waiting for confirmation');
  });

  test('imported hours', () => {
    const d = draw('imported', fixture('imported'));
    expect(text(d)).toContain('Move all clear matches');
    expect(text(d)).toContain('Aria, Gabriel');
    expect(d.querySelector('input[name=person][value=p1]')).not.toBeNull();
    expect(d.querySelector('form[action="/time/imported/e9"]')).not.toBeNull();
  });
});
