import { describe, expect, it } from 'vitest';
import { draw, text } from './testing/draw';
import { fixture } from './testing/fixture';

describe('the CRM CSV import page', () => {
  it('offers the three kinds and a check before an import', () => {
    const d = draw('crmimport', fixture('crmimport'));
    const kinds = [...d.querySelectorAll('select[name=kind] option')].map((o) => o.getAttribute('value'));
    expect(kinds).toEqual(['companies', 'contacts', 'deals']);
    const modes = [...d.querySelectorAll('button[name=mode]')].map((b) => b.getAttribute('value'));
    expect(modes).toEqual(['check', 'import']);
    expect(d.querySelector('form[action="/crm/import"]')?.getAttribute('enctype')).toBe('multipart/form-data');
    expect(d.querySelector('[data-crm-import-result]')).toBeNull();
  });

  it('shows what a check found: counts, the columns read and left out, and each problem with its line', () => {
    const d = draw('crmimport', fixture('crmimport-checked'));
    const t = text(d);
    expect(t).toContain('Checked, nothing was created.');
    expect(d.querySelector('[data-created]')?.getAttribute('data-created')).toBe('3');
    expect(d.querySelector('[data-skipped]')?.getAttribute('data-skipped')).toBe('1');
    expect(d.querySelector('[data-failed]')?.getAttribute('data-failed')).toBe('2');
    expect(t).toContain('Deal → Name');
    expect(t).toContain('Owner region, Lead source');
    expect(t).toContain('Line 3:');
    expect(t).toContain('1 new companies would be made');
    expect(t).not.toContain('See what came in');
  });
});
