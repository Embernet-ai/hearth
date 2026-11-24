<script lang="ts">
  // @ts-nocheck
  let { pg }: { pg: any; onEnv?: any } = $props();
  const r: any = $derived(pg.Desk?.Report);
  const d: any = $derived(pg.Desk);
  const money = (a: any[]) => (a?.length ? a.map((x) => x.Text).join(' + ') : '0');
  const days = (n: number) => {
    const t = new Date();
    t.setDate(t.getDate() - n);
    return t.toISOString().slice(0, 10);
  };
  const cents = (a: any[]) => (a || []).reduce((t: number, x: any) => t + (x.Cents || 0), 0);
  const maxOf = (rows: any[], f: (x: any) => number) => Math.max(1, ...(rows || []).map(f));
  const pct = (v: number, m: number) => Math.round((v / m) * 100) + '%';
  const range = (n: number) => '/crm/reports?from=' + days(n) + '&to=' + days(0) + (pg.OwnerSel ? '&owner=' + pg.OwnerSel : '');
</script>

<section>
  <div class="row spread">
    <h1>Sales reports</h1>
    <div class="row"><a class="chip" href="/crm/forecast">Forecast</a></div>
  </div>
  <form class="row wrap" method="get" action="/crm/reports" data-report-filters>
    <input name="from" type="date" value={d.From} aria-label="From" />
    <input name="to" type="date" value={d.To} aria-label="To" />
    <select name="owner" aria-label="Owner">
      <option value="">Everyone</option>
      <option value="none" selected={pg.OwnerSel === 'none'}>Nobody</option>
      {#each pg.Owners || [] as o}<option value={o.PrincipalID} selected={o.PrincipalID === pg.OwnerSel}>{o.DisplayName}</option>{/each}
    </select>
    <button type="submit" class="ghost small">Show</button>
    <a class="chip" href={range(29)}>30 days</a>
    <a class="chip" href={range(89)}>90 days</a>
    <a class="chip" href={range(364)}>Year</a>
  </form>
  <div class="row wrap" data-report-totals>
    <div class="card"><div class="muted small">Open pipeline ({r.OpenCount})</div><strong>{money(r.Open)}</strong></div>
    <div class="card"><div class="muted small">Weighted</div><strong>{money(r.Weighted)}</strong></div>
    <div class="card"><div class="muted small">Won ({r.WonCount})</div><strong>{money(r.Won)}</strong></div>
    <div class="card"><div class="muted small">Lost or no response ({r.LostCount})</div><strong>{money(r.Lost)}</strong></div>
    <div class="card"><div class="muted small">Win rate</div><strong>{r.WinRate < 0 ? 'none closed' : r.WinRate + '%'}</strong></div>
    <div class="card"><div class="muted small">Average won deal</div><strong>{money(r.AvgWon)}</strong></div>
    <div class="card"><div class="muted small">Days from made to won</div><strong>{r.WonCount ? r.AvgCycleDays : 'none won'}</strong></div>
  </div>
  {#if d.Quoted}
    <h2>Quoted in EmberOps</h2>
    <p data-report-quoted>{d.Quoted.Count} quotations in this period, {money(d.Quoted.ByCurrency)}. {#each d.Quoted.ByStatus || [] as s}<span class="tag">{s.Status} {s.Count}</span> {/each}</p>
  {/if}
  <h2>Where the money is</h2>
  <div class="bars" data-chart-stages>
    {#each r.Pipeline as s}
      <div class="barrow"><span class="barlabel">{s.Label}</span>
        <span class="bar" style={'width:' + pct(cents(s.Value), maxOf(r.Pipeline, (x) => cents(x.Value)))} title={money(s.Value)}></span>
        <span class="muted small">{money(s.Value)} ({s.Count})</span></div>
    {/each}
  </div>
  <h2>How long deals have sat in their stage</h2>
  <table class="list" data-report-age>
    <thead><tr><th>Stage</th><th>Open deals</th><th>Average days</th><th>Oldest, days</th></tr></thead>
    <tbody>{#each r.StageAge || [] as a}<tr><td>{a.Label}</td><td>{a.Count}</td><td>{a.AvgDays}</td><td>{a.OldestDays}</td></tr>
      {:else}<tr><td colspan="4" class="muted">No open deals.</td></tr>{/each}</tbody>
  </table>
  <h2>Why deals were lost</h2>
  <div class="bars" data-chart-lost>
    {#each r.LostReasons || [] as l}
      <div class="barrow"><span class="barlabel">{l.Reason}</span>
        <span class="bar lost" style={'width:' + pct(l.Count, maxOf(r.LostReasons, (x) => x.Count))}></span>
        <span class="muted small">{l.Count} deals, {money(l.Value)}</span></div>
    {:else}<p class="muted">No deals lost in this period.</p>{/each}
  </div>
  <h2>Pipeline by stage</h2>
  <table class="list" data-report-stages>
    <thead><tr><th>Stage</th><th>Chance</th><th>Deals</th><th>Value</th><th>Weighted</th></tr></thead>
    <tbody>{#each r.Pipeline as s}<tr><td><a href={'/deals?view=table&stage=' + s.Stage}>{s.Label}</a></td><td>{s.Probability}%</td><td>{s.Count}</td><td>{money(s.Value)}</td><td>{money(s.Weighted)}</td></tr>{/each}</tbody>
  </table>
  <h2>By person</h2>
  <table class="list" data-report-owners>
    <thead><tr><th>Owner</th><th>Open</th><th>Open value</th><th>Weighted</th><th>Won</th><th>Won value</th><th>Lost</th></tr></thead>
    <tbody>{#each r.Owners || [] as o}<tr><td>{o.Name}</td><td>{o.OpenCount}</td><td>{money(o.Open)}</td><td>{money(o.Weighted)}</td><td>{o.WonCount}</td><td>{money(o.Won)}</td><td>{o.LostCount}</td></tr>{/each}</tbody>
  </table>
  <h2>Activity</h2>
  <table class="list" data-report-activity>
    <thead><tr><th>Person</th><th>Notes</th><th>Calls</th><th>Meetings</th><th>Emails</th><th>Total</th></tr></thead>
    <tbody>
      {#each r.Activity || [] as a}<tr><td>{a.Name}</td><td>{a.Notes}</td><td>{a.Calls}</td><td>{a.Meetings}</td><td>{a.Emails}</td><td>{a.Total}</td></tr>
      {:else}<tr><td colspan="6" class="muted">Nothing logged in this period.</td></tr>{/each}
    </tbody>
  </table>
  <h2>Deals made, by month</h2>
  <table class="list" data-report-created>
    <thead><tr><th>Month</th><th>Deals</th><th>Value</th></tr></thead>
    <tbody>{#each r.Created || [] as m}<tr><td>{m.Month}</td><td>{m.Count}</td><td>{money(m.Value)}</td></tr>{/each}</tbody>
  </table>
</section>
