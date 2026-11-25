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
    expect(labels('CRM', page({ SalesNav: true }))).toEqual(['Contacts', 'Companies', 'Deals', 'Follow-ups', 'Forecast', 'Sales reports', 'Unassigned', 'Duplicates', 'Custom fields']);
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

describe('a contact in Mautic', () => {
  const base = { Contact: { ID: 'c1', Name: 'Dana' }, Companies: [], Activities: [], Owners: [], Details: {}, SalesOn: true };
  test('shows the segments with a way in or out, and the ones the sync keeps without one', () => {
    const d = draw('contact', page({ ...base, Desk: { Followups: [], Today: '2026-11-24', Mautic: { OptedOut: true, Segments: [{ ID: 1, Name: 'Expo', Member: false, Locked: false }, { ID: 2, Name: 'Hearth: all contacts', Member: true, Locked: true }], Campaigns: [{ ID: 7, Name: 'Welcome', Member: true }] } } }));
    expect(d.querySelectorAll('[data-mautic-segments] form').length).toBe(1);
    expect(text(d)).toContain('kept by the sync');
    expect(d.querySelector('[data-mautic-optout]')).not.toBeNull();
    expect(d.querySelector('[data-mautic-campaigns] input[name="on"]')?.getAttribute('value')).toBe('0');
  });
  test('says when the person is not in Mautic yet', () => {
    const d = draw('contact', page({ ...base, Desk: { Followups: [], Today: '2026-11-24', MauticNote: 'This person is not in Mautic yet. The marketing sync adds anybody with an email address.' } }));
    expect(text(d)).toContain('not in Mautic yet');
  });
});

describe('a contacts form responses', () => {
  test('are listed with the question and the answer', () => {
    const d = draw('contact', page({ Contact: { ID: 'c1', Name: 'Dana' }, Companies: [], Activities: [], Owners: [], Details: {}, SalesOn: true,
      Desk: { Followups: [], Today: '2026-11-24', Forms: [{ Form: 'Expo lead form', Created: '2026-11-01T10:00:00Z', Finished: true, Answers: [{ Question: 'What do you need?', Answer: 'Rewinds' }] }] } }));
    expect(d.querySelector('[data-form-responses]')?.textContent).toContain('What do you need?: Rewinds');
  });
});

describe('the reports depth', () => {
  test('draws the money by stage, how long deals have sat, and why they were lost', () => {
    const d = draw('salesreports', page({ Owners: [], Desk: { From: '2026-11-01', To: '2026-11-30', Report: { OpenCount: 2, Open: [{ Text: '$3,000', Cents: 300000 }], Weighted: [], WonCount: 0, Won: [], LostCount: 3, Lost: [], WinRate: 0, AvgWon: [], AvgCycleDays: 0,
      Pipeline: [{ Label: 'Meeting', Stage: 'meeting', Probability: 40, Count: 1, Value: [{ Text: '$1,000', Cents: 100000 }], Weighted: [] }, { Label: 'Proposal', Stage: 'proposal', Probability: 60, Count: 1, Value: [{ Text: '$2,000', Cents: 200000 }], Weighted: [] }],
      StageAge: [{ Label: 'Meeting', Stage: 'meeting', Count: 1, AvgDays: 12, OldestDays: 12 }],
      LostReasons: [{ Reason: 'Price', Count: 2, Value: [{ Text: '$500' }] }, { Reason: 'No reason given', Count: 1, Value: [{ Text: '$100' }] }], Owners: [], Activity: [], Created: [] } } }));
    const bars = d.querySelectorAll('[data-chart-stages] .bar');
    expect(bars.length).toBe(2);
    expect(bars[1].getAttribute('style')).toContain('100%');
    expect(bars[0].getAttribute('style')).toContain('50%');
    expect(d.querySelector('[data-report-age]')?.textContent).toContain('12');
    expect(d.querySelector('[data-chart-lost]')?.textContent).toContain('Price');
    expect(d.querySelector('[data-chart-lost] .bar')?.getAttribute('style')).toContain('100%');
  });
  test('says so when nothing was lost', () => {
    const d = draw('salesreports', page({ Owners: [], Desk: { From: '2026-11-01', To: '2026-11-30', Report: { OpenCount: 0, Open: [], Weighted: [], WonCount: 0, Won: [], LostCount: 0, Lost: [], WinRate: -1, AvgWon: [], AvgCycleDays: 0, Pipeline: [], StageAge: [], LostReasons: [], Owners: [], Activity: [], Created: [] } } }));
    expect(text(d)).toContain('No deals lost in this period.');
  });
});

describe('the timeline', () => {
  const acts = [{ ID: 'a1', Kind: 'call', Title: 'Rang', Direction: 'outbound', Disposition: 'voicemail', OccurredAt: '2026-11-20T15:00:00Z', ByName: 'Pat' }, { ID: 'a2', Kind: 'note', Title: 'Noted', Body: 'x', OccurredAt: '2026-11-21T15:00:00Z' }];
  const drawn = () => draw('deal', page({ Path: '/deals/d1', Deal: { ID: 'd1', Name: 'Big', Stage: 'new' }, DealStages: ['new'], Companies: [], Contacts: [], Owners: [], SalesOn: true, Activities: acts, Desk: { Followups: [], Today: '2026-11-24' } }));
  test('has a filter by kind when there is more than one entry', () => {
    const d = drawn();
    expect(d.querySelectorAll('[data-timeline-filter]').length).toBe(5);
    expect(d.querySelector('[data-activity="a1"]')?.getAttribute('data-kind')).toBe('call');
  });
  test('each entry can be edited, a call with its direction and outcome and a note without', () => {
    const d = drawn();
    expect(d.querySelector('[data-activity="a1"] form[action="/activities/a1/edit"] select[name="direction"]')).not.toBeNull();
    expect(d.querySelector('[data-activity="a1"] input[name="disposition"]')).not.toBeNull();
    expect(d.querySelector('[data-activity="a2"] form[action="/activities/a2/edit"]')).not.toBeNull();
    expect(d.querySelector('[data-activity="a2"] select[name="direction"]')).toBeNull();
  });
});

describe('enrolling people in Mautic from the contacts list', () => {
  const rows = [{ ID: 'c1', Name: 'Dana', Email: 'd@x.example' }];
  test('offers the segments and campaigns beside the bulk bar, posting the ticked people to the enrol route', () => {
    const d = draw('contacts', page({ SalesNav: true, SalesOn: true, ContactRows: rows, Companies: [], Owners: [], SortLinks: {}, Path: '/contacts', Desk: { MauticChoices: [{ Kind: 'segment', ID: 1, Name: 'Expo' }, { Kind: 'campaign', ID: 7, Name: 'Welcome' }] } }));
    const opts = [...d.querySelectorAll('select[data-enrol-target] option')].map((o) => o.getAttribute('value'));
    expect(opts).toEqual(['segment:1', 'campaign:7']);
    expect(d.querySelector('button[data-enrol]')?.getAttribute('formaction')).toBe('/crm/mautic/enrol');
    expect(d.querySelector('form#bulkform button[data-enrol]')).not.toBeNull();
  });
  test('offers nothing when Mautic is not connected', () => {
    const d = draw('contacts', page({ SalesNav: true, SalesOn: true, ContactRows: rows, Companies: [], Owners: [], SortLinks: {}, Path: '/contacts', Desk: null }));
    expect(d.querySelector('button[data-enrol]')).toBeNull();
  });
});

describe('custom fields', () => {
  const props = [
    { ID: 'p1', Key: 'industry', Label: 'Industry', DataType: 'select', Options: ['Plating', 'Rewinds'], Text: 'Rewinds' },
    { ID: 'p2', Key: 'contract_end', Label: 'Contract end', DataType: 'date', Text: '2027-03-01' },
    { ID: 'p3', Key: 'reorder', Label: 'Reorder', DataType: 'boolean', Text: 'true' },
    { ID: 'p4', Key: 'speed', Label: 'Line speed', DataType: 'number', Text: '' },
  ];
  test('a record shows each field with the input its kind of value needs, filled in', () => {
    const d = draw('deal', page({ Deal: { ID: 'd1', Name: 'Big', Stage: 'new' }, DealStages: ['new'], Companies: [], Contacts: [], Owners: [], Activities: [], SalesOn: true, Desk: { Followups: [], Today: '2026-11-24', Properties: props } }));
    const f = d.querySelector('[data-custom-fields]')!;
    expect(f.querySelector('select[name="p_industry"] option[selected]')?.getAttribute('value')).toBe('Rewinds');
    expect(f.querySelector('input[name="p_contract_end"]')?.getAttribute('type')).toBe('date');
    expect(f.querySelector('select[name="p_reorder"] option[selected]')?.getAttribute('value')).toBe('yes');
    expect(f.querySelector('input[name="p_speed"]')).not.toBeNull();
    expect(f.querySelector('input[name="target_type"]')?.getAttribute('value')).toBe('deal');
  });
  test('a record with no custom fields shows no section', () => {
    const d = draw('deal', page({ Deal: { ID: 'd1', Name: 'Big', Stage: 'new' }, DealStages: ['new'], Companies: [], Contacts: [], Owners: [], Activities: [], SalesOn: true, Desk: { Followups: [], Today: '2026-11-24', Properties: [] } }));
    expect(text(d)).not.toContain('Custom fields');
  });
  test('an admin sees the add form and the remove buttons, somebody else only the list', () => {
    const base = { PropKind: 'deal', PropTypes: ['text', 'select'], PropDefs: [{ ID: 'p1', Key: 'industry', Label: 'Industry', DataType: 'select', Options: ['A'] }] };
    const admin = draw('properties', page({ Desk: { ...base, CanManage: true } }));
    expect(admin.querySelector('[data-new-property]')).not.toBeNull();
    expect(admin.querySelector('[data-property="industry"] button[data-confirm]')).not.toBeNull();
    const other = draw('properties', page({ Desk: { ...base, CanManage: false } }));
    expect(other.querySelector('[data-new-property]')).toBeNull();
    expect(text(other)).toContain('Only an admin adds or removes fields.');
  });
});

describe('follow-up kinds, snooze and bulk', () => {
  test('the queue shows each kind, offers snooze and a bulk action', () => {
    const d = draw('followups', page({ Owners: [], Pager: null, Path: '/crm/followups', Desk: { Who: '', Status: 'open', Due: '', Kind: 'call', Today: '2026-11-24', Followups: [
      { ID: 'f1', Kind: 'call', Title: 'Ring Dana', Status: 'open', DueAt: '2026-11-30T00:00:00Z', AssigneeName: 'Pat', Target: { Type: 'deal', ID: 'd1' }, TargetName: 'Big', TargetHref: '/deals/d1' }] } }));
    expect(d.querySelector('[data-kind="call"]')).not.toBeNull();
    expect(d.querySelector('[data-kind-filter="call"].on')).not.toBeNull();
    expect(d.querySelectorAll('[data-snooze]').length).toBe(3);
    expect(d.querySelector('form#followupbulk button[value="1"]')).not.toBeNull();
    expect(d.querySelector('input[name="ids"][form="followupbulk"]')?.getAttribute('value')).toBe('f1');
  });
  test('the record form offers the kinds and the list shows a kind that is not a plain to do', () => {
    const d = draw('deal', page({ Deal: { ID: 'd1', Name: 'Big', Stage: 'new' }, DealStages: ['new'], Companies: [], Contacts: [], Owners: [], Activities: [], SalesOn: true,
      Desk: { Today: '2026-11-24', Followups: [{ ID: 'f1', Kind: 'meeting', Title: 'Demo', Status: 'open', DueAt: '2026-11-30T00:00:00Z' }] } }));
    expect(d.querySelectorAll('[data-add-followup] select[name="kind"] option').length).toBe(4);
    expect(d.querySelector('[data-followup="f1"] [data-kind="meeting"]')).not.toBeNull();
  });
});

describe('filtering a list by a custom field', () => {
  const desk = { PropKind: 'company', PropSel: 'industry', PropVal: 'Rewinds', PropDefs: [{ Key: 'industry', Label: 'Industry' }, { Key: 'speed', Label: 'Line speed' }] };
  test('the companies list offers the fields and keeps the filter in use', () => {
    const d = draw('companies', page({ SalesNav: true, SalesOn: true, CompanyRows: [], Companies: [], Owners: [], SortLinks: {}, CompanyKinds: [], Path: '/companies', Desk: desk }));
    const sel = d.querySelector('select[data-field-filter]')!;
    expect([...sel.querySelectorAll('option')].map((o) => o.getAttribute('value'))).toEqual(['', 'industry', 'speed']);
    expect(sel.querySelector('option[selected]')?.getAttribute('value')).toBe('industry');
    expect(d.querySelector('input[data-field-value]')?.getAttribute('value')).toBe('Rewinds');
  });
  test('a list with no custom fields shows no filter', () => {
    const d = draw('companies', page({ SalesNav: true, SalesOn: true, CompanyRows: [], Companies: [], Owners: [], SortLinks: {}, CompanyKinds: [], Path: '/companies', Desk: null }));
    expect(d.querySelector('select[data-field-filter]')).toBeNull();
  });
});

describe('the deals board', () => {
  const base = { SalesOn: true, SalesNav: true, Companies: [], Owners: [], DealStages: ['new'], SortLinks: {}, ViewMode: 'board', SearchText: 'acme', OwnerSel: 'p1' };
  test('shows each column weighted by its stage chance, and none for a closed stage', () => {
    const d = draw('deals', page({ ...base, Filter: 'all', DealCols: [{ Stage: 'proposal', Label: 'Proposal', Count: 2, Total: 200000, Probability: 60, Weighted: 120000, Deals: [] }, { Stage: 'won', Label: 'Won', Count: 1, Total: 5000, Probability: 100, Weighted: 5000, Deals: [] }] }));
    expect(d.querySelectorAll('[data-weighted]').length).toBe(1);
    expect(d.querySelector('[data-weighted]')?.textContent).toContain('60% chance');
  });
  test('has chips that narrow it and keep the search and owner, the one in use lit', () => {
    const d = draw('deals', page({ ...base, Filter: 'overdue', DealCols: [] }));
    expect(d.querySelector('[data-chip="overdue"].on')).not.toBeNull();
    expect(d.querySelector('[data-chip="all"].on')).toBeNull();
    const href = d.querySelector('[data-chip="noclose"]')!.getAttribute('href')!;
    expect(href).toContain('q=acme');
    expect(href).toContain('owner=p1');
    expect(href).toContain('noclose=1');
    expect(d.querySelector('[data-chip="month"]')!.getAttribute('href')).toMatch(/closefrom=\d{4}-\d{2}-01&closeto=\d{4}-\d{2}-\d{2}/);
  });
});
