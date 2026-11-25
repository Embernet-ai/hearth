// Go's template builtins and Hearth's template functions, in TypeScript, for the pages that were ported from the Go templates.
// Each is a port of the Go function of the same name and prints what it printed: gofuncs.test.mjs holds them to the answers Go
// gave. Times arrive as the RFC 3339 text Go encodes them with, in the server's zone, and are formatted from that text, never
// through the browser's zone, so a page shows the same hour it always did.


type Any = any;

/** A named string, number or bool that carries methods arrives as {"$": value, Method: result}; this is the value. */
const u = (v: Any): Any => (v !== null && typeof v === 'object' && !Array.isArray(v) && '$' in v ? v.$ : v);

/** Go's truthiness: false, 0, "", nil, and an empty array, map or string are false. */
export function t(v: Any): boolean {
  v = u(v);
  if (v === null || v === undefined || v === false || v === 0 || v === '') return false;
  if (Array.isArray(v)) return v.length > 0;
  if (typeof v === 'object') return Object.keys(v).length > 0;
  return true;
}

/** How a template prints a value. */
export function S(v: Any): string {
  v = u(v);
  if (v === null || v === undefined) return '';
  if (typeof v === 'object') return Array.isArray(v) ? v.map(S).join(' ') : '';
  return String(v);
}

const pad = (n: number, w = 2) => String(n).padStart(w, '0');
const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'];

interface Parts { y: number; mo: number; d: number; h: number; mi: number; s: number }
function parts(v: Any): Parts | null {
  if (typeof v !== 'string') return null;
  const m = /^(\d{4})-(\d{2})-(\d{2})(?:[T ](\d{2}):(\d{2})(?::(\d{2}))?)?/.exec(v);
  if (!m) return null;
  return { y: +m[1], mo: +m[2], d: +m[3], h: +(m[4] ?? 0), mi: +(m[5] ?? 0), s: +(m[6] ?? 0) };
}
function fmt(v: Any, layout: string): string {
  const p = parts(v);
  if (!p) return '';
  switch (layout) {
    case '2 Jan 2006': return `${p.d} ${MONTHS[p.mo - 1].slice(0, 3)} ${pad(p.y, 4)}`;
    case 'January 2006': return `${MONTHS[p.mo - 1]} ${pad(p.y, 4)}`;
    case '2': return String(p.d);
    case '15:04': return `${pad(p.h)}:${pad(p.mi)}`;
    case '2006-01-02': return `${pad(p.y, 4)}-${pad(p.mo)}-${pad(p.d)}`;
    case '2006-01-02T15:04': return `${pad(p.y, 4)}-${pad(p.mo)}-${pad(p.d)}T${pad(p.h)}:${pad(p.mi)}`;
  }
  throw new Error('time layout ' + layout);
}

const num = (v: Any) => (typeof v === 'number' ? v : Number(v) || 0);
const noMin = (v: Any) => v === null || v === undefined;


// ---- docs.Query (internal/docs/query.go) and the view functions that build links from it (internal/web/views.go).
// A Query arrives under its Go field names, like every other value in a page (the templates read .Query.SpaceID):
// Kind, SpaceID, Filters[{Field{Column, Property}, Op, Values}], Sorts[{Field, Desc}], GroupBy, Ungrouped, Columns, Closed, Offset.
interface Field { Column?: string; Property?: string }
const fieldKey = (f: Field | undefined) => (f?.Property ? 'prop.' + f.Property : f?.Column ?? '');
const fieldZero = (f: Field | undefined) => !f || (!f.Column && !f.Property);
const sameField = (a: Field, b: Field) => (a?.Column ?? '') === (b?.Column ?? '') && (a?.Property ?? '') === (b?.Property ?? '');

const COLUMN_LABELS: Record<string, string> = {
  title: 'Title', kind: 'Kind', owner: 'Owner', space: 'Space', created: 'Created', updated: 'Updated', status: 'Status',
  priority: 'Priority', assignee: 'Assignee', due: 'Due', started: 'Started', completed: 'Completed',
};

/** url.QueryEscape: unreserved are A-Z a-z 0-9 - _ . ~ ; a space is "+"; everything else is %XX, upper case. */
function qesc(s: string): string {
  let out = '';
  for (const b of new TextEncoder().encode(s)) {
    const c = String.fromCharCode(b);
    if (/[A-Za-z0-9\-_.~]/.test(c)) out += c;
    else if (b === 0x20) out += '+';
    else out += '%' + b.toString(16).toUpperCase().padStart(2, '0');
  }
  return out;
}

/** Query.Encode() as url.Values: key to list of values. */
function encodeQuery(q: Any): Map<string, string[]> {
  const v = new Map<string, string[]>();
  const set = (k: string, x: string) => v.set(k, [x]);
  const add = (k: string, x: string) => v.set(k, [...(v.get(k) ?? []), x]);
  if (q?.Kind) set('kind', q.Kind);
  if (q?.SpaceID != null) { set('space', q.SpaceID); if (!q?.Direct) set('inside', '1'); }
  if (q?.Closed) set('closed', '1');
  for (const f of q?.Filters ?? []) {
    let s = fieldKey(f.Field) + ':' + f.Op;
    if ((f.Values ?? []).length > 0) s += ':' + f.Values.join(',');
    add('f', s);
  }
  if ((q?.Sorts ?? []).length > 0) set('sort', q.Sorts.map((x: Any) => fieldKey(x.Field) + (x.Desc ? ':desc' : '')).join(','));
  if (!fieldZero(q?.GroupBy)) set('group', fieldKey(q.GroupBy));
  else if (q?.Ungrouped) set('group', '');
  if ((q?.Columns ?? []).length > 0) set('cols', q.Columns.map(fieldKey).join(','));
  if ((q?.Offset ?? 0) > 0) set('offset', String(q.Offset));
  return v;
}
/** url.Values.Encode(): keys sorted, each value escaped. */
function encodeValues(v: Map<string, string[]>): string {
  const keys = [...v.keys()].sort();
  const parts: string[] = [];
  for (const k of keys) for (const x of v.get(k)!) parts.push(qesc(k) + '=' + qesc(x));
  return parts.join('&');
}
function replaceFilter(q: Any, field: Field, value: string): Any {
  const Filters = (q?.Filters ?? []).filter((f: Any) => !sameField(f.Field, field));
  if (value !== '') Filters.push({ Field: field, Op: 'is', Values: [value] });
  return { ...q, Filters };
}
function queryString(q: Any, viewKind: string): string {
  const v = encodeQuery(q);
  v.delete('kind');
  if (viewKind && viewKind !== 'table') v.set('view', [viewKind]);
  return v.size === 0 ? '' : '?' + encodeValues(v);
}
const queryHas = (q: Any, field: Field, value: string) =>
  (q?.Filters ?? []).some((f: Any) => sameField(f.Field, field) && (f.Values ?? []).length === 1 && f.Values[0] === value);

// ---- time.Time.Format for the layouts the templates use (Go reference time: Mon Jan 2 15:04:05 2006)
const DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
function goFormat(v: Any, layout: string): string {
  const p = parts(v);
  if (!p) return '';
  // Date.UTC reads years 0 to 99 as 1900 to 1999; setUTCFullYear does not (Go's zero time is year 1).
  const dt = new Date(0);
  dt.setUTCFullYear(p.y, p.mo - 1, p.d);
  const dow = dt.getUTCDay();
  const h12 = p.h % 12 === 0 ? 12 : p.h % 12;
  const tokens: [string, string][] = [
    ['January', MONTHS[p.mo - 1]], ['Monday', DAYS[dow]], ['2006', pad(p.y, 4)], ['Jan', MONTHS[p.mo - 1].slice(0, 3)],
    ['Mon', DAYS[dow].slice(0, 3)], ['MST', 'UTC'], ['01', pad(p.mo)], ['02', pad(p.d)], ['03', pad(h12)], ['04', pad(p.mi)],
    ['05', pad(p.s)], ['06', pad(p.y % 100)], ['15', pad(p.h)], ['PM', p.h >= 12 ? 'PM' : 'AM'], ['pm', p.h >= 12 ? 'pm' : 'am'],
    ['1', String(p.mo)], ['2', String(p.d)], ['3', String(h12)], ['4', String(p.mi)], ['5', String(p.s)],
  ];
  let out = '';
  for (let i = 0; i < layout.length; ) {
    const hit = tokens.find(([t]) => layout.startsWith(t, i));
    if (hit) { out += hit[1]; i += hit[0].length; } else out += layout[i++];
  }
  return out;
}

const labelOf = (f: Field, defs: Any[]) => {
  if (!f?.Property) return COLUMN_LABELS[f?.Column ?? ''] ?? f?.Column ?? '';
  return (defs ?? []).find((d) => d.Key === f.Property)?.Label ?? f.Property;
};

export const F: Record<string, (...a: Any[]) => Any> = {
  t,
  // ---- Go's builtins
  and: (...a) => { for (const x of a) if (!t(x)) return x; return a[a.length - 1]; },
  or: (...a) => { for (const x of a) if (t(x)) return x; return a[a.length - 1]; },
  not: (x) => !t(x),
  eq: (a, ...rest) => ((a = u(a)), rest.map(u)).some((b) => a === b || (a !== null && b !== null && typeof a !== 'object' && typeof b !== 'object' && String(a) === String(b) && typeof a === typeof b)),
  ne: (a, b) => !(u(a) === u(b)),
  lt: (a, b) => u(a) < u(b),
  le: (a, b) => u(a) <= u(b),
  gt: (a, b) => u(a) > u(b),
  ge: (a, b) => u(a) >= u(b),
  len: (x) => (x === null || x === undefined ? 0 : typeof x === 'string' || Array.isArray(x) ? x.length : typeof x === 'object' ? Object.keys(x).length : 0),
  index: (x, ...ks) => { let v = x; for (const k of ks) { if (v === null || v === undefined) return undefined; v = v[S(k)]; } return v; },
  printf: (f: string, ...a) => { let i = 0; return f.replace(/%([sdvq]|\.\d+f)/g, (_m, c) => { const v = a[i++]; return c === 'q' ? JSON.stringify(S(v)) : c.endsWith('f') ? Number(v).toFixed(+c.slice(1, -1)) : S(v); }); },
  print: (...a) => a.map(S).join(''),
  // range: Go ranges a slice by index and a map by sorted key
  rng: (x) => {
    if (x === null || x === undefined) return [];
    if (Array.isArray(x)) return x.map((v, i) => [i, v]);
    if (typeof x === 'object') return Object.keys(x).sort().map((k) => [k, x[k]]);
    return [];
  },
  // a string of attributes ("checked", or name="value") as an attribute object
  attrs: (str) => { const o: Any = {}; for (const m of S(str).matchAll(/([\w:-]+)(?:="([^"]*)")?/g)) o[m[1]] = m[2] ?? true; return o; },
  // a method on a Go value: only the ones templates call
  m: (recv, name, ...a) => {
    if (name === 'Has' && a.length === 2) return queryHas(recv, a[0], a[1]); // docs.Query.Has(field, value)
    // authz.Held.Has: a person with nothing granted arrives as null (a nil map), and Go answers false for it
    if (name === 'Has' && a.length === 1) return !!(recv && !Array.isArray(recv) && (recv.super_admin || recv[S(a[0])]));
    if (name === 'Format') return goFormat(recv, a[0]); // time.Time.Format
    // methods with arguments, on the page value or a request (internal/web/subtaskpage.go, deptrequestpage.go, deptreq.go)
    if (name === 'AskedHas') return (recv?.Asked ?? []).some((d: Any) => d.ID === a[0]);
    if (name === 'IsAssigned') return (recv?.AssigneeIDs ?? []).includes(a[0]);
    if (name === 'IsPrimaryAssignee') return !!a[0] && a[0] === recv?.AssigneePrimary;
    if (name === 'DeptTicked') {
      const r = recv?.MyDeptRequest;
      if (r && r.Status === 'pending') return (r.Asked ?? []).some((d: Any) => d.ID === a[0]);
      return (recv?.MyDepartments ?? []).some((d: Any) => d.ID === a[0]);
    }
    if (name === 'In') return Array.isArray(recv?.Departments) ? recv.Departments.some((d: Any) => d.ID === a[0]) : false;
    throw new Error('method ' + name + ' is not ported');
  },

  // ---- Hearth's functions
  // ---- views.go
  col: (name) => ({ Column: name, Property: '' }),
  prop: (key) => ({ Column: '', Property: key }),
  filterValue: (f) => fieldKey(f.Field) + ':' + f.Op + ((f.Values ?? []).length > 0 ? ':' + f.Values.join(',') : ''),
  hasFilter: (q, f) => (q?.Filters ?? []).some((x: Any) => sameField(x.Field, f)),
  withStatus: (q, viewKind, status) => queryString(replaceFilter(q, { Column: 'status' }, status), viewKind),
  clearStatus: (q, viewKind) => queryString(replaceFilter(q, { Column: 'status' }, ''), viewKind),
  toggleClosed: (q, viewKind) => queryString({ ...q, Closed: !q?.Closed }, viewKind),
  toggleProp: (q, viewKind, key, value) => {
    const f = { Property: key };
    return queryString(replaceFilter(q, f, queryHas(q, f, value) ? '' : value), viewKind);
  },
  sortedBy: (q, key, desc) => (q?.Sorts ?? []).length > 0 && fieldKey(q.Sorts[0].Field) === key && !!q.Sorts[0].Desc === !!desc,
  fieldLabel: (f, defs) => labelOf(f, defs),
  isClosedStatus: (v) => v === 'done' || v === 'cancelled',
  short: (v) => (S(v).length > 8 ? S(v).slice(0, 8) : S(v)),
  deref_s: (v) => (noMin(v) ? '' : v),
  deref: (v) => (noMin(v) ? 0 : v),
  same: (a, b) => !noMin(b) && a === b,
  i64p: (v) => v,
  list: (...a) => a,
  dict: (...kv) => { const o: Any = {}; for (let i = 0; i + 1 < kv.length; i += 2) if (typeof kv[i] === 'string') o[kv[i]] = kv[i + 1]; return o; },
  join: (v) => (v ?? []).join(', '),
  joinlines: (v) => (v ?? []).join('\n'),
  hasPrefix: (s, p) => S(s).startsWith(S(p)),
  firstLine: (v) => S(v).split('\n')[0],
  div: (a, b) => (b === 0 ? 0 : Math.trunc(a / b)),
  capped: (p) => (p > 100 ? 100 : p < 0 ? 0 : p),
  px: (v) => `${v}px`,
  pct: (v) => `${Number(v).toFixed(4)}%`,
  indent: (d) => `${(Math.min(Math.max(d, 0), 12) * 1.1).toFixed(1)}rem`,
  qty: (v) => String(num(v)),
  meter: (v) => String(num(v)),
  money: (c) => (noMin(c) ? '' : (num(c) / 100).toFixed(2)),
  cost: (c) => (num(c) / 100).toFixed(2),
  dollars: (c) => (noMin(c) ? '' : (num(c) / 100).toFixed(2)),
  clock: (m) => `${pad(Math.trunc(m / 60))}:${pad(m % 60)}`,
  clockp: (m) => (noMin(m) ? '' : `${pad(Math.trunc(m / 60))}:${pad(m % 60)}`),
  hhmm: (m) => (noMin(m) ? '' : `${Math.trunc(m / 60)}:${pad(m % 60)}`),
  hours: (m) => {
    if (m <= 0) return 'none';
    if (m < 60) return `${m}m`;
    if (m % 60 === 0) return `${m / 60}h`;
    return `${Math.trunc(m / 60)}h ${m % 60}m`;
  },
  duration: (ns) => {
    const s = ns / 1e9;
    if (s < 1) return 'under a second';
    if (s < 60) return `${Math.trunc(s)}s`;
    if (s < 3600) return `${Math.trunc(s / 60)}m ${Math.trunc(s) % 60}s`;
    return `${Math.trunc(s / 3600)}h ${Math.trunc(s / 60) % 60}m`;
  },
  dur: (ms) => {
    if (noMin(ms) || ms <= 0) return '';
    const sec = Math.trunc(ms / 1000);
    return sec < 60 ? `${sec}s` : `${Math.trunc(sec / 60)}m ${pad(sec % 60)}s`;
  },
  date: (v) => fmt(v, '2 Jan 2006'),
  day: (v) => fmt(v, '2'),
  month: (v) => fmt(v, 'January 2006'),
  timeofday: (v) => fmt(v, '15:04'),
  datefield: (v) => fmt(v, '2006-01-02'),
  dateonly: (v) => fmt(v, '2006-01-02'),
  dateinput: (v) => fmt(v, '2006-01-02'),
  dtlocal: (v) => fmt(v, '2006-01-02T15:04'),
  ago: (v) => {
    if (v === null || v === undefined) return 'never';
    const p = parts(v);
    if (!p) return '';
    // time.Since saturates at the largest Duration (about 292 years), so the zero time reads 106751d ago, not 739000d.
    const d = Math.min(Date.now() - Date.parse(v), 9223372036854);
    if (d < 60e3) return 'just now';
    if (d < 3600e3) return `${Math.trunc(d / 60e3)}m ago`;
    if (d < 86400e3) return `${Math.trunc(d / 3600e3)}h ago`;
    return `${Math.trunc(d / 86400e3)}d ago`;
  },
  priority: (p) => (p >= 3 ? 'urgent' : p === 2 ? 'high' : p === 1 ? 'normal' : 'low'),
  fileKind: (c) => (S(c).startsWith('video/') ? 'video' : S(c).startsWith('image/') ? 'image' : 'doc'),
  isVideo: (c) => S(c).startsWith('video/'),
  writable: (k) => k === 'doc' || k === 'daily_report',
  hasTaskFields: (k) => k === 'task' || k === 'work_order',
  statusLabel: (s) => ({ todo: 'To do', in_progress: 'In progress', blocked: 'Blocked', done: 'Done', cancelled: 'Cancelled' } as Any)[s] ?? s,
  stageName: (s) => STAGES[s] ?? s,
  leaveKind: (k) => KINDS[k] ?? k,
  usd: (c, cur) => {
    const sym = !cur || cur === 'USD' ? '$' : cur + ' ';
    const whole = Math.trunc(c / 100);
    const s = String(whole).replace(/\B(?=(\d{3})+(?!\d))/g, ',');
    return c % 100 !== 0 ? `${sym}${s}.${pad(c % 100)}` : sym + s;
  },
  interval: (s) => {
    const parts: string[] = [];
    if (s?.EveryDays != null) {
      const d = s.EveryDays;
      parts.push(d === 1 ? 'daily' : d === 7 ? 'weekly' : d % 7 === 0 ? `every ${d / 7} weeks` : `every ${d} days`);
    }
    if (s?.MeterKey != null && s?.EveryUnits != null) parts.push(`every ${s.EveryUnits} ${s.MeterKey}`);
    return parts.length === 0 ? 'no interval' : parts.length === 1 ? parts[0] : `${parts[0]}, or ${parts[1]}, whichever comes first`;
  },
  // Markdown is rendered by the server (markdown.Render); a page that needs it asks for the rendered text.
  markdown: () => { throw new Error('markdown is rendered on the server'); },
};

const STAGES: Record<string, string> = {
  new: 'New', screening: 'Screening', meeting: 'Meeting', proposal: 'Proposal', customer: 'Customer', won: 'Won', lost: 'Lost', no_response: 'No response',
};
const KINDS: Record<string, string> = {
  vacation: 'Vacation leave', sick: 'Sick leave', personal: 'Personal leave', medical: 'Medical leave', family: 'Family leave',
  bereavement: 'Bereavement leave', jury_duty: 'Jury duty leave', other: 'Other',
};

/** Sets an attribute with setAttribute. Svelte assigns `form` on an input as a property, which is read only. */
export const A = (name: string, value: Any) => (node: Element) => { node.setAttribute(name, S(value)); };
