<script lang="ts">
  // @ts-nocheck
  let { pg }: { pg: any; onEnv?: any } = $props();
  const f: any = $derived(pg.Desk?.Forecast);
  const money = (a: any[]) => (a?.length ? a.map((x) => x.Text).join(' + ') : '0');
  const chance = (s: string) => ({ new: 10, screening: 20, meeting: 40, proposal: 60, customer: 90 })[s] ?? 0;
</script>

<section>
  <div class="row spread">
    <h1>Forecast</h1>
    <div class="row"><a class="chip" href={pg.ExportURL}>Export CSV</a><a class="chip" href="/crm/reports">Sales reports</a></div>
  </div>
  <form class="row wrap" method="get" action="/crm/forecast" data-forecast-filters>
    <select name="owner" aria-label="Owner" data-autosubmit>
      <option value="">Everyone</option>
      <option value="none" selected={pg.OwnerSel === 'none'}>Nobody</option>
      {#each pg.Owners || [] as o}<option value={o.PrincipalID} selected={o.PrincipalID === pg.OwnerSel}>{o.DisplayName}</option>{/each}
    </select>
    <button type="submit" class="ghost small">Filter</button>
  </form>
  <div class="row wrap" data-forecast-totals>
    <div class="card"><div class="muted small">Open deals</div><strong>{f.Count}</strong></div>
    <div class="card"><div class="muted small">Pipeline</div><strong>{money(f.Value)}</strong></div>
    <div class="card"><div class="muted small">Weighted by chance of closing</div><strong>{money(f.Weighted)}</strong></div>
    <div class="card"><div class="muted small">Won this month ({f.WonCount})</div><strong>{money(f.Won)}</strong></div>
  </div>
  {#each f.Buckets || [] as b (b.Key)}
    <h2 data-bucket={b.Key}>{b.Label} <span class="muted small">{b.Count} deals · {money(b.Value)} · weighted {money(b.Weighted)}</span></h2>
    <table class="list">
      <thead><tr><th>Deal</th><th>Stage</th><th>Chance</th><th>Amount</th><th>Close date</th><th>Company</th><th>Owner</th></tr></thead>
      <tbody>
        {#each b.Deals as d (d.ID)}
          <tr data-deal={d.ID}>
            <td><a href={'/deals/' + d.ID}>{d.Name}</a></td>
            <td><span class="tag">{d.Stage}</span></td>
            <td class="muted small">{chance(d.Stage)}%</td>
            <td class="muted small">{(d.AmountCents / 100).toLocaleString(undefined, { style: 'currency', currency: d.Currency || 'USD' })}</td>
            <td class="muted small">{d.CloseDate ? d.CloseDate.slice(0, 10) : ''}</td>
            <td class="muted small">{d.CompanyName}</td>
            <td class="muted small">{d.OwnerName}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p class="muted">No open deals.</p>
  {/each}
  <h2>By person</h2>
  <table class="list" data-forecast-owners>
    <thead><tr><th>Owner</th><th>Open deals</th><th>Pipeline</th><th>Weighted</th></tr></thead>
    <tbody>
      {#each f.Owners || [] as o}
        <tr><td>{o.Name}</td><td>{o.Count}</td><td>{money(o.Value)}</td><td>{money(o.Weighted)}</td></tr>
      {/each}
    </tbody>
  </table>
</section>
