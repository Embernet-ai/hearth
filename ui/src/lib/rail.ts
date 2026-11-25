import type { Page } from './types';

// The navigation, item for item and gate for gate what internal/web/templates/layout.html draws.
// Nothing is added and nothing is dropped: a person sees in the rebuilt rail exactly the
// destinations the rail has always shown them, because both read the same capabilities from the
// same page value.

export interface RailItem {
  label: string;
  href: string;
  /** Pages under this path keep the item lit. Defaults to the href itself. */
  on?: (p: string) => boolean;
  show?: (pg: Page) => boolean;
  badge?: (pg: Page) => number | undefined;
}

export interface RailGroup {
  key: string;
  heading: string;
  dot: 'work' | 'plan' | 'talk' | 'know';
  items: RailItem[];
}

const eq = (...paths: string[]) => (p: string) => paths.includes(p);
const pre = (prefix: string) => (p: string) => p.startsWith(prefix);
// authz.Held.Has: super admin implies everything.
const can = (cap: string) => (pg: Page) => !!(pg.Can?.super_admin || pg.Can?.[cap]);

export const scan: RailItem = { label: 'Scan', href: '/scan' };

export const mine: RailGroup = {
  key: 'mine',
  heading: 'Mine',
  dot: 'work',
  items: [
    { label: 'Dashboard', href: '/' },
    { label: 'Inbox', href: '/inbox', badge: (pg) => pg.Unread },
    { label: 'Outbox', href: '/outbox', badge: (pg) => pg.Undecided },
  ],
};

export const groups: RailGroup[] = [
  {
    key: 'g-crm',
    heading: 'CRM',
    dot: 'talk',
    items: [
      { label: 'Contacts', href: '/contacts', on: pre('/contacts') },
      { label: 'Companies', href: '/companies', on: pre('/companies') },
      { label: 'Deals', href: '/deals', on: pre('/deals'), show: (pg) => !!pg.SalesNav },
      { label: 'Follow-ups', href: '/crm/followups', show: (pg) => !!pg.SalesNav },
      { label: 'Forecast', href: '/crm/forecast', show: (pg) => !!pg.SalesNav },
      { label: 'Sales reports', href: '/crm/reports', show: (pg) => !!pg.SalesNav },
      { label: 'Unassigned', href: '/crm/unassigned', show: (pg) => !!pg.SalesNav },
      { label: 'Duplicates', href: '/crm/duplicates', show: (pg) => !!pg.SalesNav },
      { label: 'Custom fields', href: '/crm/properties', show: (pg) => !!pg.SalesNav },
    ],
  },
  {
    // Team chat is not the CRM, so it has its own place. It was the first line of the group that is now the CRM.
    key: 'g-chat',
    heading: 'Chat',
    dot: 'talk',
    items: [{ label: 'Channels', href: '/channels', on: pre('/channels') }],
  },
  {
    key: 'g-work',
    heading: 'Work',
    dot: 'know',
    items: [
      { label: 'Work orders', href: '/work-orders' },
      { label: 'Tasks', href: '/tasks' },
      { label: 'All documents', href: '/documents' },
      { label: 'Projects', href: '/projects' },
      { label: 'Spaces & folders', href: '/spaces' },
      { label: 'Views', href: '/views' },
      { label: 'My time', href: '/time' },
      { label: 'Daily reports', href: '/reports/daily', on: eq('/reports/daily', '/reports/daily/new') },
      { label: 'Service history', href: '/reports/service' },
      { label: 'Tags', href: '/tags' },
      { label: 'Calendar', href: '/calendar' },
      { label: 'Workload', href: '/workload', show: can('time_all') },
      { label: 'Time report', href: '/reports/time', show: can('finance') },
      { label: 'Time off', href: '/time-off', badge: (pg) => pg.TimeOffPending },
      {
        label: 'Approvals',
        href: '/time/approvals',
        show: (pg) => !!pg.ApprovalsWaiting || pg.Path === '/time/approvals',
        badge: (pg) => pg.ApprovalsWaiting,
      },
    ],
  },
  {
    key: 'g-apps',
    heading: 'Marketing',
    dot: 'talk',
    items: [
      { label: 'Marketing', href: '/marketing' },
      { label: 'Forms', href: '/forms' },
      { label: 'Social', href: '/social' },
      { label: 'Tools', href: '/tools', show: (pg) => !!pg.ToolsOn },
    ],
  },
  {
    key: 'g-plant',
    heading: 'Plant',
    dot: 'know',
    items: [
      { label: 'Assets', href: '/assets' },
      { label: 'Maintenance', href: '/maintenance' },
      { label: 'Plant tree', href: '/plant' },
      { label: 'Parts', href: '/parts' },
      { label: 'Templates', href: '/templates' },
      { label: 'Canvas', href: '/canvas' },
    ],
  },
];

const settingsPaths = ['/settings', '/account', '/people', '/teams', '/rules', '/agents', '/settings/mail', '/integrations',
  '/audit', '/ops/jobs', '/time/imported', '/settings/tutorial', '/departments/request'];

export const tail: RailItem[] = [
  { label: 'Settings', href: '/settings', on: (p) => settingsPaths.includes(p) },
  { label: 'Import', href: '/import' },
];

export function isOn(item: RailItem, path: string): boolean {
  return item.on ? item.on(path) : path === item.href;
}

/**
 * What a contractor account's rail offers: the work in the folder it was given, and nothing about the company. The server
 * refuses every other address to such an account (internal/web/contractorgate.go), so this is only the rail not offering
 * what would answer "no such page".
 */
export const contractorHrefs = new Set(['/inbox', '/tasks', '/work-orders', '/documents', '/projects', '/spaces', '/views']);

/** The items of a group this page's reader is offered: a contractor account only the work, everybody else by their capabilities. */
export function visibleItems(items: RailItem[], pg: Page): RailItem[] {
  const contractor = !!pg.Identity?.Contractor;
  return items.filter((i) => (!contractor || contractorHrefs.has(i.href)) && (!i.show || i.show(pg)));
}
