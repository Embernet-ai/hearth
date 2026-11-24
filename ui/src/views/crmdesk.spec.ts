import { describe, expect, test } from 'vitest';
import { groups, visibleItems } from '../lib/rail';
import { draw, page, text } from './testing/draw';

const labels = (g: string, pg: any) => visibleItems(groups.find((x) => x.heading === g)!.items, pg).map((i) => i.label);

describe('the CRM group of the rail', () => {
  test('is called CRM, and team chat has its own group', () => {
    expect(groups.map((g) => g.heading)).not.toContain('Conversation');
    expect(groups.map((g) => g.heading).slice(0, 2)).toEqual(['CRM', 'Chat']);
    expect(labels('Chat', page())).toEqual(['Channels']);
  });
  test('offers the sales pages to a person who works sales', () => {
    expect(labels('CRM', page({ SalesNav: true }))).toEqual(['Contacts', 'Companies', 'Deals', 'Follow-ups', 'Forecast', 'Sales reports', 'Unassigned']);
  });
  test('offers a person outside sales only the contacts and companies they always had', () => {
    expect(labels('CRM', page({ SalesNav: false }))).toEqual(['Contacts', 'Companies']);
  });
});

const owners = [{ PrincipalID: 'p1', DisplayName: 'Pat' }];

describe('the follow-up queue', () => {
  test('lists what is owed, everybody by default, with the filters', () => {
    const d = draw('followups', page({ Owners: owners, Pager: null, Path: '/crm/followups', Desk: { Who: '', Status: 'open', Due: '', Today: '2026-11-24', Followups: [
      { ID: 'f1', Title: 'Send the quote', Status: 'open', DueAt: '2026-11-20T00:00:00Z', Overdue: true, AssigneeName: 'Pat', Target: { Type: 'deal', ID: 'd1' }, TargetName: 'Big deal', TargetHref: '/deals/d1' }] } }));
    expect(text(d)).toContain('Send the quote');
    expect(text(d)).toContain('Everyone');
    expect(d.querySelector('[data-followup="f1"].overdue')).not.toBeNull();
    expect(d.querySelector('a[href="/deals/d1"]')).not.toBeNull();
  });
});

describe('the forecast and the report', () => {
  test('the forecast shows buckets, totals and people', () => {
    const d = draw('forecast', page({ Owners: owners, ExportURL: '/crm/forecast.csv', Desk: { Forecast: { Count: 1, Value: [{ Text: '$1,000' }], Weighted: [{ Text: '$600' }], WonCount: 0, Won: [],
      Buckets: [{ Key: '2026-12', Label: 'December 2026', Count: 1, Value: [{ Text: '$1,000' }], Weighted: [{ Text: '$600' }], Deals: [{ ID: 'd1', Name: 'Big deal', Stage: 'proposal', AmountCents: 100000, Currency: 'USD', CloseDate: '2026-12-10T00:00:00Z' }] }],
      Owners: [{ Name: 'Pat', Count: 1, Value: [{ Text: '$1,000' }], Weighted: [{ Text: '$600' }] }] } } }));
    expect(text(d)).toContain('December 2026');
    expect(text(d)).toContain('$600');
    expect(text(d)).toContain('60%');
  });
  test('the report says when nothing closed', () => {
    const d = draw('salesreports', page({ Owners: owners, Desk: { From: '2026-10-26', To: '2026-11-24', Report: { OpenCount: 0, Open: [], Weighted: [], WonCount: 0, Won: [], LostCount: 0, Lost: [], WinRate: -1, AvgWon: [], AvgCycleDays: 0, Pipeline: [], Owners: [], Activity: [], Created: [] } } }));
    expect(text(d)).toContain('none closed');
    expect(text(d)).toContain('Nothing logged in this period.');
  });
});
