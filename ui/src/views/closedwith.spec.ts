import { describe, expect, it } from 'vitest';
import { draw } from './testing/draw';
import { fixture } from './testing/fixture';

const words = (e: Element) => (e.textContent ?? '').replace(/\s+/g, ' ').trim();

describe('a task closed with its subtasks says so', () => {
  it('shows the line above the title, as a status, when the page was reached after a close that took subtasks', () => {
    const d = draw('document', fixture('document-time-refused', { Flash: 'Closed with 3 subtasks' }));
    const line = d.querySelector('p[role=status]')!;
    expect(words(line)).toBe('Closed with 3 subtasks');
    expect(line.compareDocumentPosition(d.querySelector('h1')!) & 4).toBeTruthy(); // the title follows it
  });

  it('shows nothing extra on an ordinary visit, and keeps the status form and the subtasks section', () => {
    const d = draw('document', fixture('document-time-refused'));
    expect(d.querySelector('p[role=status]')).toBeNull();
    expect(d.querySelector('select[name=status]')).not.toBeNull();
    expect(d.querySelector('#subtasks')).not.toBeNull();
  });
});
