import { describe, expect, test } from 'vitest';
import { groups, mine, tail, visibleItems } from '../lib/rail';
import { page } from './testing/draw';

const hrefs = (pg: any) => [...mine.items, ...groups.flatMap((g) => g.items), ...tail].filter((i) => visibleItems([i], pg).length).map((i) => i.href);

describe('the rail', () => {
  // A contractor account is offered the work in its folder and nothing about the company: no contacts, deals, channels,
  // calendar, people, time, plant or settings.
  test('offers a contractor account only the work', () => {
    const pg: any = page({ Identity: { PrincipalID: 'c1', DisplayName: 'Carol', Contractor: true }, SalesNav: true, Can: { super_admin: false } });
    expect(hrefs(pg).sort()).toEqual(['/documents', '/inbox', '/projects', '/spaces', '/tasks', '/views', '/work-orders']);
    for (const gone of ['/contacts', '/deals', '/crm/unassigned', '/channels', '/calendar', '/reports/daily', '/reports/service', '/time', '/time-off', '/assets', '/settings', '/import']) {
      expect(hrefs(pg), `${gone} is offered to a contractor`).not.toContain(gone);
    }
  });

  // Anybody else is offered exactly what they always were: the same rail, item for item.
  test('is unchanged for everybody else', () => {
    const ordinary: any = page({ Identity: { PrincipalID: 'p1', DisplayName: 'Pat' }, SalesNav: true });
    const all = [...mine.items, ...groups.flatMap((g) => g.items), ...tail].filter((i) => !i.show || i.show(ordinary)).map((i) => i.href);
    expect(hrefs(ordinary)).toEqual(all);
    for (const kept of ['/contacts', '/deals', '/channels', '/calendar', '/reports/daily', '/time', '/assets', '/settings', '/import', '/documents']) {
      expect(hrefs(ordinary), `${kept} went missing for an ordinary account`).toContain(kept);
    }
  });
});
