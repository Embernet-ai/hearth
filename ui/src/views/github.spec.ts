import { describe, expect, test } from 'vitest';
import { draw, text } from './testing/draw';
import { fixture } from './testing/fixture';

describe('the GitHub page', () => {
  test('before connecting it explains the read only link and asks for a token', () => {
    const d = draw('github', fixture('github-none'));
    expect(text(d)).toContain('never changes');
    const form = d.querySelector('form[action="/github/connect"]');
    expect(form).not.toBeNull();
    expect(form?.querySelector('input[name=token][type=password]')).not.toBeNull();
    expect(d.querySelector('form[action="/github/repos"]')).toBeNull();
  });

  test('connected it shows the numbers and the repositories, and can only choose or disconnect', () => {
    const d = draw('github', fixture('github-connected'));
    expect(text(d)).toContain('octo');
    expect(d.querySelectorAll('[data-totals] .stat').length).toBe(4);
    expect(d.querySelectorAll('[data-repos] tbody tr').length).toBe(1);
    expect(d.querySelectorAll('input[name=repo]').length).toBe(2);
    expect((d.querySelector('input[name=repo]') as HTMLInputElement).checked).toBe(true);
    // every form on the page posts to Hearth's own /github routes: there is nothing that writes to GitHub
    const actions = [...d.querySelectorAll('form')].map((f) => f.getAttribute('action'));
    expect(actions.every((a) => a?.startsWith('/github/'))).toBe(true);
  });

  test('the dashboard carries the person\'s GitHub numbers', () => {
    const d = draw('dashboard', fixture('dashboard-full'));
    expect(d.querySelector('.recap .g-github')).not.toBeNull();
    expect(d.querySelectorAll('.recap .g-github a.stat').length).toBe(4);
  });
});
