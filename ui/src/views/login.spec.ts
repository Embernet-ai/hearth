import { describe, expect, test } from 'vitest';
import { render } from 'svelte/server';
import { Window } from 'happy-dom';
import { view } from './index';

// The sign-in page is the EmberOps door. An install with local accounts keeps its form and its option; an install whose only
// way in is its provider (HEARTH_LOCAL_LOGIN_FORM=false) shows the provider button and nothing else.
function door(page: Record<string, unknown>): Document {
  const { body } = render(view('login') as any, { props: { env: { page }, onEnv: () => {} } });
  const win = new Window();
  win.document.body.innerHTML = body;
  return win.document as unknown as Document;
}
const text = (d: Document) => (d.body.textContent ?? '').replace(/\s+/g, ' ').trim();

describe('login door', () => {
  test('a provider-only install shows one button and no form', () => {
    const d = door({ SSO: true, SSOName: 'Vord', HideLocalLogin: true });
    expect(text(d)).toContain('Login to Hearth');
    expect(d.querySelectorAll('a.door-btn').length).toBe(1);
    expect(d.querySelector('a.door-btn')?.getAttribute('href')).toBe('/login/sso');
    expect(text(d)).toContain('Login with Vord');
    expect(d.querySelector('img.door-btn-icon')?.getAttribute('src')).toBe('/vord.svg');
    expect(d.querySelector('form'), 'the form is off').toBeNull();
    expect(d.querySelector('input'), 'no field on the page').toBeNull();
  });

  test('local accounts keep the form, and the provider button sits under it', () => {
    const d = door({ SSO: true, SSOName: 'Vord', HideLocalLogin: false, Next: '/tasks' });
    expect(d.querySelector('form[action="/login"]')).not.toBeNull();
    expect(d.querySelector('input[name="email"]')).not.toBeNull();
    expect(d.querySelector('input[name="password"]')).not.toBeNull();
    expect(d.querySelector('input[name="next"]')?.getAttribute('value')).toBe('/tasks');
    expect(d.querySelectorAll('a.door-btn').length).toBe(1);
  });

  test('an install with no provider has the form and no provider button', () => {
    const d = door({ SSO: false });
    expect(d.querySelector('form[action="/login"]')).not.toBeNull();
    expect(d.querySelector('a.door-btn')).toBeNull();
  });

  test('the form is never taken away when there is no provider to replace it', () => {
    const d = door({ SSO: false, HideLocalLogin: true });
    expect(d.querySelector('form[action="/login"]')).not.toBeNull();
  });

  test('an unnamed provider reads single sign-on and shows no Vord icon', () => {
    const d = door({ SSO: true, HideLocalLogin: true });
    expect(text(d)).toContain('Login with single sign-on');
    expect(d.querySelector('img.door-btn-icon')).toBeNull();
  });

  test('a refusal is shown on the door', () => {
    const d = door({ SSO: true, SSOName: 'Vord', HideLocalLogin: true, Message: 'No access' });
    expect(d.querySelector('[role="alert"]')?.textContent).toBe('No access');
  });
});
