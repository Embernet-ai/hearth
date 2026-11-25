import { describe, expect, test } from 'vitest';
import { draw, text } from './testing/draw';
import { fixture } from './testing/fixture';

// A department page's right-hand side is the department as a directory, drawn from the same tree as the side panel.
describe('a department as a directory', () => {
  const d = draw('documents', fixture('documents-dept'));

  test('its folders are the tree\'s children, in the tree\'s order, with the tree\'s counts and nesting', () => {
    const entries = [...d.querySelectorAll('[data-dir-entries] > li.direntry')];
    expect(entries.map((e) => e.querySelector('.dirname')?.textContent)).toEqual(['Embernet', 'White Team', 'Red Team']);
    expect(entries.map((e) => e.querySelector('.dircount')?.textContent)).toEqual(['12', '88', '3']);
    const sub = [...entries[0].querySelectorAll('.dirsub li')].map((l) => l.textContent?.replace(/\s+/g, ' ').trim());
    expect(sub).toEqual(['Platform40', 'Releases7']);
    expect(entries[0].querySelector('a.dirlink')?.getAttribute('href')).toBe('/documents?space=e-emb');
    expect(d.querySelector('[data-dir-entries] [data-space="e-plat"] a')?.getAttribute('href')).toBe('/documents?space=e-plat');
  });

  test('the page is headed by the department, not Everything', () => {
    expect(d.querySelector('h1')?.textContent).toBe('Engineering');
  });

  test('the count says what the default view leaves out, so it agrees with the side panel', () => {
    const a = d.querySelector('a[data-closed-hidden]');
    expect(a?.textContent).toBe('1 closed');
    expect(a?.getAttribute('href')).toBe('/documents?closed=1&space=e-eng');
  });

  test('projects stay reachable under the folders', () => {
    expect(d.querySelector('[data-dir-projects] a[href="/documents?space=p-click"]')).not.toBeNull();
  });

  test('a breadcrumb goes back up, and the level can be widened to everything inside', () => {
    const path = d.querySelector('[data-dir-path]')!;
    expect(path.querySelector('a[href="/documents"]')?.textContent).toBe('Departments');
    expect(path.querySelector('strong')?.textContent).toBe('Engineering');
    expect(d.querySelector('a[data-dir-inside]')?.getAttribute('href')).toBe('/documents?space=e-eng&inside=1');
  });

  test('the filters are not a wall of chips: one Filter control holds status, properties and views', () => {
    const toolbar = d.querySelector('.toolbar')!;
    expect(toolbar.querySelectorAll(':scope > .viewsbar, :scope > nav.filters').length).toBe(0);
    expect(toolbar.querySelector('button[popovertarget="pop-filter"]')?.textContent).toBe('Filter');
    expect(d.querySelector('#pop-filter nav.filters')).not.toBeNull();
    // everything that was reachable still is
    for (const t of ['Group by', 'Columns', 'Sort', 'Show closed', 'Save view']) expect(text(d)).toContain(t);
    expect(d.querySelectorAll('nav.viewtabs a').length).toBe(2);
  });
});

describe('a folder two levels down', () => {
  const d = draw('documents', fixture('documents-sub'));
  test('shows the breadcrumb up to the top and its own folders, and says it is the whole level', () => {
    const path = d.querySelector('[data-dir-path]')!;
    expect([...path.querySelectorAll('a')].map((a) => a.textContent)).toEqual(['Departments', 'Engineering', 'Embernet']);
    expect(path.querySelector('strong')?.textContent).toBe('Platform');
    expect([...d.querySelectorAll('[data-dir-entries] .dirname')].map((e) => e.textContent)).toEqual(['API']);
    expect(d.querySelector('a[data-dir-here]')?.getAttribute('href')).toBe('/documents?space=e-plat');
  });
});
