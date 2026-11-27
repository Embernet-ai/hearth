import { describe, expect, test } from 'vitest';
import { isFile, isPage, withoutFormat } from './api';

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

// The time report's Download buttons did nothing in the interface: the format is in the query, so the address looked like a page.
describe('files named by format in the query', () => {
  test('a csv or xlsx request is a file, not a page', () => {
    expect(isFile('/reports/time?from=2026-11-01&format=xlsx')).toBe(true);
    expect(isFile('/reports/time?person=p1&format=csv&approval=pending')).toBe(true);
    expect(isPage('/reports/time?detail=1&all=1&format=csv')).toBe(false);
    expect(isPage('/reports/time?format=xlsx')).toBe(false);
  });
  test('the report page itself is still a page', () => {
    expect(isPage('/reports/time?from=2026-11-01&to=2026-11-30&approval=pending')).toBe(true);
    expect(isFile('/reports/time?note=format%3Dcsv')).toBe(false);
  });
});

describe('returning from sign-in to a file', () => {
  test('the report page is the address without the format', () => {
    expect(withoutFormat('/reports/time?from=2026-11-01&format=xlsx&approval=pending')).toBe('/reports/time?from=2026-11-01&approval=pending');
    expect(withoutFormat('/reports/time?format=csv')).toBe('/reports/time');
  });
});
