import { describe, expect, test } from 'vitest';
import { isPage } from './api';

// The interface posts every form to a page through fetch and draws the answer. A post that answers with a redirect to another
// site (Connect my Google Workspace account sends the person to Google to say yes) cannot be followed by fetch, so the service
// worker showed "That one needs a connection" and the button never worked. Those posts are left to the browser.
describe('isPage', () => {
  test('a connect post that leaves for the provider is not a page', () => {
    expect(isPage('/integrations/google/connect')).toBe(false);
    expect(isPage('/integrations/microsoft/connect')).toBe(false);
  });
  test('the pages around it still are', () => {
    expect(isPage('/settings/connectors')).toBe(true);
    expect(isPage('/integrations/google/disconnect')).toBe(true);
    expect(isPage('/integrations/google/sync')).toBe(true);
  });
});
