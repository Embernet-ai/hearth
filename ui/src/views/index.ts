import type { Component } from 'svelte';
import type { Page, Envelope } from '../lib/types';
import Login from './Login.svelte';
import NotFound from './NotFound.svelte';

type View = Component<{ pg: Page; onEnv: (e: Envelope, url: string) => void }>;

// A page's view is the Svelte file named for the template the server says it is drawn from:
// pages/documents.svelte draws "documents". shared.svelte holds snippets the pages import, not a page.
const files = import.meta.glob<{ default: View }>('./pages/*.svelte', { eager: true });
const registry: Record<string, View> = {};
for (const [file, mod] of Object.entries(files)) {
  const name = file.slice('./pages/'.length, -'.svelte'.length);
  if (name !== 'shared' && mod.default) registry[name] = mod.default;
}
// Sign in is written by hand: it is where a session starts, so it has its own handling of the redirect after it.
registry.login = Login as unknown as View;

/** The view for a template name. A name the server sends that has no view is a build mistake, and says so. */
export function view(template: string): View {
  return registry[template] ?? (NotFound as unknown as View);
}
