import { render } from 'svelte/server';
import { Window } from 'happy-dom';
import { view } from '../index';

/** The base of every page's data: a signed-in person, nothing granted. A test says what else is true. */
export function page(extra: Record<string, unknown> = {}) {
  return {
    Title: '', Path: '/', Version: '', Message: '', Today: '2026-11-17',
    Identity: { PrincipalID: 'me', OrgID: 'o', DisplayName: 'Me', Subjects: [], MustChangePassword: false },
    Build: { Revision: 'test', Profile: 'test' },
    Can: {}, IsAdmin: false, SalesNav: true,
    ...extra,
  };
}

/** Draw the view for a template with this page data, and return what a person would see as a document. */
export function draw(template: string, pg: Record<string, unknown>): Document {
  const { body } = render(view(template) as any, { props: { pg, onEnv: () => {} } });
  const win = new Window();
  win.document.body.innerHTML = body;
  return win.document as unknown as Document;
}

/** The text a person reads, whitespace folded. */
export function text(d: Document): string {
  return (d.body.textContent ?? '').replace(/\s+/g, ' ').trim();
}
