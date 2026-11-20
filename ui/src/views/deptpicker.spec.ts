import { describe, expect, it } from 'vitest';
import { render } from 'svelte/server';
import { Window } from 'happy-dom';
import { draw } from './testing/draw';
import { fixture } from './testing/fixture';
import OrgNode from '../components/OrgNode.svelte';

const words = (e: Element) => (e.textContent ?? '').replace(/\s+/g, ' ').trim();

describe('the department picker on My departments', () => {
  it('has a Save button for somebody who may place people, and takes effect at once', () => {
    const d = draw('deptrequest', fixture('deptrequest-pending', { DeptApplyNow: true, MyDeptRequest: null }));
    const form = d.querySelector('form[action="/departments/request"]')!;
    expect(form.querySelectorAll('input[name=space]').length).toBe(2);
    const save = form.querySelector('button[type=submit]')!;
    expect(words(save)).toBe('Save my departments');
    expect(words(form)).toContain('then save. It takes effect at once');
    expect(form.querySelector('#note')).toBeNull(); // nobody to write a note to
  });

  it('has a Send request button, and a note, for everybody else', () => {
    const d = draw('deptrequest', fixture('deptrequest-pending', { DeptApplyNow: false, MyDeptRequest: null }));
    const form = d.querySelector('form[action="/departments/request"]')!;
    expect(words(form.querySelector('button[type=submit]')!)).toBe('Send request');
    expect(form.querySelector('#note')).not.toBeNull();
  });

  it('ticks the departments a person is already in', () => {
    const d = draw('deptrequest', fixture('deptrequest-pending', { DeptApplyNow: true, MyDepartments: [{ ID: 'e1', Name: 'Engineering' }], MyDeptRequest: null }));
    const boxes = [...d.querySelectorAll('input[name=space]')] as HTMLInputElement[];
    expect(boxes.map((b) => b.hasAttribute('checked'))).toEqual([true, false]);
  });
});

describe('the Departments panel', () => {
  const tree = {
    ID: 'eng', Name: 'Engineering', Documents: 1, Open: true,
    Children: [
      { ID: 'wt', Name: 'White Team', Documents: 3, Children: [{ ID: 'sp', Name: 'Standard Products' }] },
      { ID: 'rt', Name: 'Red Team' },
    ],
  };
  const drawNode = () => {
    const { body } = render(OrgNode as any, { props: { node: tree, canManage: false, level: 0 } });
    const win = new Window();
    win.document.body.innerHTML = body;
    return win.document as unknown as Document;
  };

  it('draws the hierarchy: a folder that holds folders collapses and expands, one that holds none is a plain row', () => {
    const d = drawNode();
    const outer = d.querySelector('details.orgnode.lvl0')!;
    expect(outer.hasAttribute('open')).toBe(true); // the path to the open node
    expect(outer.querySelector(':scope > summary .chev')).not.toBeNull(); // the control to collapse it
    expect(outer.querySelector('details.orgnode.lvl1 > summary')).not.toBeNull(); // White Team nests and collapses too
    expect(outer.querySelector('span.navrow.lvl1')).not.toBeNull(); // Red Team is a leaf, no empty group
    expect(outer.querySelector('span.navrow.lvl2')).not.toBeNull(); // Standard Products one level further in
  });

  it('keeps every name, its link and its document count, and nothing else', () => {
    const d = drawNode();
    const rows = [...d.querySelectorAll('a.navitem.dept')].map((a) => [a.getAttribute('title'), a.getAttribute('href'), words(a.querySelector('.ncount') ?? a.ownerDocument.createElement('i'))]);
    expect(rows).toEqual([
      ['Engineering', '/documents?space=eng', '1'],
      ['White Team', '/documents?space=wt', '3'],
      ['Standard Products', '/documents?space=sp', ''],
      ['Red Team', '/documents?space=rt', ''],
    ]);
  });
});
