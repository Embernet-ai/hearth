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
    expect(labels('CRM', page({ SalesNav: true }))).toEqual(['Contacts', 'Companies', 'Deals', 'Follow-ups', 'Forecast', 'Sales reports', 'Unassigned', 'Duplicates']);
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

describe('the duplicates page', () => {
  test('offers each group with a keeper and the others to merge in', () => {
    const d = draw('duplicates', page({ Desk: { DupType: 'contact', Dups: [{ Reason: 'Same phone number', Records: [{ ID: 'a', Name: 'Dana', Detail: 'dana@x.example', Owner: 'Pat', Deals: 2 }, { ID: 'b', Name: 'Dana W', Detail: '', Owner: '', Deals: 0 }] }] } }));
    expect(text(d)).toContain('Same phone number');
    expect(d.querySelectorAll('[data-dup-group] input[name="merge"]').length).toBe(2);
    expect(d.querySelector('input[name="survivor"][value="a"]')).not.toBeNull();
    expect(d.querySelector('a[href="/contacts/b"]')).not.toBeNull();
  });
  test('says so when there are none', () => {
    expect(text(draw('duplicates', page({ Desk: { DupType: 'company', Dups: [] } })))).toContain('No duplicates found.');
  });
});

describe('what EmberOps adds to a record', () => {
  const panel = { Quotes: [{ Name: 'SAL-QTN-1', Party: 'Acme', Status: 'Open', Total: 1250.5, Currency: 'USD', ValidTill: '2026-12-10', URL: 'https://ops.example/app/quotation/SAL-QTN-1' }],
    Calls: [{ Name: 'CL-1', Type: 'Incoming', Status: 'Completed', From: '+12165550142', To: '100', Start: '2026-11-20 10:00:00', Seconds: 125, URL: 'https://ops.example/app/call-log/CL-1' }], NewQuoteURL: 'https://ops.example/app/quotation/new?opportunity=CRM-OPP-1' };
  test('a deal shows its quotes, its calls and a way to make a quote', () => {
    const d = draw('deal', page({ Deal: { ID: 'd1', Name: 'Big', Stage: 'proposal' }, DealStages: ['new', 'proposal'], Companies: [], Contacts: [], Owners: [], Activities: [], SalesOn: true, Desk: { Followups: [], Today: '2026-11-24', EmberOps: panel } }));
    expect(d.querySelector('[data-emberops-quotes] a[href="https://ops.example/app/quotation/SAL-QTN-1"]')).not.toBeNull();
    expect(d.querySelector('[data-emberops-calls]')?.textContent).toContain('3 min');
    expect(d.querySelector('a[data-new-quote]')?.getAttribute('href')).toContain('opportunity=CRM-OPP-1');
  });
  test('a deal with nothing from EmberOps shows no EmberOps heading', () => {
    const d = draw('deal', page({ Deal: { ID: 'd1', Name: 'Big', Stage: 'new' }, DealStages: ['new'], Companies: [], Contacts: [], Owners: [], Activities: [], SalesOn: true, Desk: { Followups: [], Today: '2026-11-24' } }));
    expect(text(d)).not.toContain('In EmberOps');
  });
  test('the report shows what was quoted', () => {
    const d = draw('salesreports', page({ Owners: [], Desk: { From: '2026-11-01', To: '2026-11-30', Quoted: { Count: 3, ByCurrency: [{ Text: '$1,349.50' }], ByStatus: [{ Status: 'Open', Count: 2 }] }, Report: { OpenCount: 0, Open: [], Weighted: [], WonCount: 0, Won: [], LostCount: 0, Lost: [], WinRate: -1, AvgWon: [], AvgCycleDays: 0, Pipeline: [], Owners: [], Activity: [], Created: [] } } }));
    expect(d.querySelector('[data-report-quoted]')?.textContent).toContain('3 quotations');
  });
});
