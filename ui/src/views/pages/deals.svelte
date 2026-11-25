<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  import { fieldfilter, bulkbar, ownerselect, pager, groupselect, grouprow, crmviewsbar } from './shared.svelte';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
  // The board's narrowing chips keep the search and the owner and change only what they are about.
  const chip = (extra: Record<string, string>) => {
    const u = new URLSearchParams();
    if (dot?.ViewMode === 'table') u.set('view', 'table');
    if (dot?.SearchText) u.set('q', dot.SearchText);
    if (dot?.OwnerSel) u.set('owner', dot.OwnerSel);
    for (const [k, v] of Object.entries(extra)) u.set(k, v);
    return '/deals' + (u.toString() ? '?' + u : '');
  };
  const month = () => {
    const n = new Date();
    const f = (d: Date) => d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' + String(d.getDate()).padStart(2, '0');
    return { closefrom: f(new Date(n.getFullYear(), n.getMonth(), 1)), closeto: f(new Date(n.getFullYear(), n.getMonth() + 1, 0)), open: '1' };
  };
</script>

{#snippet content(dot: any)}
<section>
  <div  class="row spread">
    <h1>Deals</h1>
    <div  class="row">
      <a  class={"chip" + S((F.t(F.ne(dot?.ViewMode, "table")) ? (" on") : ""))}  href={("/deals")}>Board</a>
      <a  class={"chip" + S((F.t(F.eq(dot?.ViewMode, "table")) ? (" on") : ""))}  href={("/deals?view=table")}>Table</a>
      <a  class="chip"  href={(S(dot?.ExportURL))}>Export CSV</a>
      <a  class="chip"  href={("/crm/import")}>Import CSV</a>
      <a  class="chip"  href={("/crm/favorites")}>Favorites</a>
    </div>
  </div>
  <p  class="row wrap"  data-deal-chips>
    <a  class={"chip" + (dot?.Filter === 'all' ? ' on' : '')}  href={chip({})}  data-chip="all">All deals</a>
    <a  class={"chip" + (dot?.Filter === 'overdue' ? ' on' : '')}  href={chip({ overdue: '1' })}  data-chip="overdue">Past their close date</a>
    <a  class={"chip" + (dot?.Filter === 'month' ? ' on' : '')}  href={chip(month())}  data-chip="month">Closing this month</a>
    <a  class={"chip" + (dot?.Filter === 'noclose' ? ' on' : '')}  href={chip({ noclose: '1', open: '1' })}  data-chip="noclose">No close date</a>
  </p>
  <form  class="row wrap"  method="get"  action="/deals"  data-deal-filters>
    {#if F.t(F.eq(dot?.ViewMode, "table"))}<input  type="hidden"  name="view"  value="table">{/if}
    <input  name="q"  value={S(dot?.SearchText)}  placeholder="Deal, company, or contact"  aria-label="Search">
    {#if F.t(F.eq(dot?.ViewMode, "table"))}
    <select  name="stage"  aria-label="Stage">
      <option  value="">Any stage</option>
      {#each F.rng(dot?.DealStages) as [k1, d1]}<option  value={S(d1)}  {...(F.t(F.eq(d1, dot?.DealStage)) ? {"selected": true} : {})}>{F.stageName(d1)}</option>{/each}
    </select>
    {/if}
    {@render fieldfilter(dot)}
    <select  name="owner"  aria-label="Owner">
      <option  value="">Any owner</option>
      <option  value="none"  {...(F.t(F.eq(dot?.OwnerSel, "none")) ? {"selected": true} : {})}>Nobody</option>
      {#each F.rng(dot?.Owners) as [k2, d2]}<option  value={S(d2?.PrincipalID)}  {...(F.t(F.eq(d2?.PrincipalID, dot?.OwnerSel)) ? {"selected": true} : {})}>{d2?.DisplayName}</option>{/each}
    </select>
    {#if F.t(F.eq(dot?.ViewMode, "table"))}{@render groupselect(dot)}{/if}
    <button  type="submit"  class="ghost small">Filter</button>
  </form>
  {@render crmviewsbar(dot)}

  {#if F.t(F.eq(dot?.ViewMode, "table"))}
    {#if F.t(dot?.Deals)}
    <table  class="list"  data-deal-table>
      <thead><tr>
        <th></th>
        <th><a  href={(S(F.index(dot?.SortLinks, "name")))}>Deal</a></th>
        <th><a  href={(S(F.index(dot?.SortLinks, "stage")))}>Stage</a></th>
        <th><a  href={(S(F.index(dot?.SortLinks, "amount")))}>Amount</a></th>
        <th><a  href={(S(F.index(dot?.SortLinks, "close")))}>Close date</a></th>
        <th><a  href={(S(F.index(dot?.SortLinks, "company")))}>Company</a></th>
        <th>Contact</th>
        <th><a  href={(S(F.index(dot?.SortLinks, "owner")))}>Owner</a></th>
      </tr></thead>
      <tbody>
        {#each F.rng(dot?.Deals) as [k3, d3]}
          {@render grouprow(dot, k3, 8)}
          <tr  data-deal={S(d3?.ID)}>
            <td><input  type="checkbox"  name="ids"  value={S(d3?.ID)}  form="bulkform"  aria-label="Select"></td>
            <td><a  href={("/deals/" + S(d3?.ID))}>{d3?.Name}</a></td>
            <td><span  class="tag">{F.stageName(d3?.Stage)}</span></td>
            <td  class="muted small">{d3?.Amount}</td>
            <td  class="muted small">{F.date(d3?.CloseDate)}</td>
            <td  class="muted small">{#if F.t(d3?.CompanyID)}<a  href={("/companies/" + S(F.deref_s(d3?.CompanyID)))}>{d3?.CompanyName}</a>{/if}</td>
            <td  class="muted small">{#if F.t(d3?.ContactID)}<a  href={("/contacts/" + S(F.deref_s(d3?.ContactID)))}>{d3?.ContactName}</a>{/if}</td>
            <td  class="muted small">{d3?.OwnerName}</td>
          </tr>
        {/each}
      </tbody>
    </table>
    {@render pager(dot?.Pager)}
    {@render bulkbar(dot, "deal")}
    {:else}
      <p  class="muted">No deals match.</p>
    {/if}
  {:else}
    
    <div  class="board"  data-group="stage"  data-move="/deals"  data-pipeline>
      {#each F.rng(dot?.DealCols) as [k4, d4]}
        <div  class="boardcol"  data-value={S(d4?.Stage)}>
          <h3>{d4?.Label} <span  class="muted small">{d4?.Count}{#if F.t(d4?.Total)} · {F.usd(d4?.Total, "USD")}{/if}</span></h3>
          {#if d4?.Total && d4?.Probability > 0 && d4?.Probability < 100}<p  class="muted small"  data-weighted>{d4.Probability}% chance, worth {F.usd(d4?.Weighted, "USD")}</p>{/if}
          <div  class="cards"  data-drop>
            {#each F.rng(d4?.Deals) as [k5, d5]}{@const v_stage = d5?.Stage}
              <article  class="card2"  draggable="true"  data-id={S(d5?.ID)}>
                <a  href={("/deals/" + S(d5?.ID))}>{d5?.Name}</a>
                <div  class="meta">
                  {#if F.t(d5?.CompanyName)}<span  class="muted small">{d5?.CompanyName}</span>{/if}
                  {#if F.t(d5?.AmountCents)}<span  class="tag">{d5?.Amount}</span>{/if}
                  {#if F.t(d5?.OwnerName)}<span  class="muted small">{d5?.OwnerName}</span>{/if}
                  {#if d5?.CloseDate}<span  class={"muted small" + (new Date(d5.CloseDate) < new Date() && !['won','lost','no_response'].includes(d5.Stage) ? " overdue" : "")}  data-close>closes {d5.CloseDate.slice(0, 10)}</span>{/if}
                  {#if d5?.StageChangedAt}<span  class="muted small"  data-age>{Math.max(0, Math.floor((Date.now() - new Date(d5.StageChangedAt).getTime()) / 864e5))}d in stage</span>{/if}
                </div>
                <form  method="post"  action={"/deals/" + S(d5?.ID) + "/move"}  class="row"  style="margin-top:.35rem">
                  <select  name="stage"  data-autosubmit aria-label="Stage">
                    {#each F.rng(dot?.DealStages) as [k6, d6]}<option  value={S(d6)}  {...(F.t(F.eq(d6, v_stage)) ? {"selected": true} : {})}>{F.stageName(d6)}</option>{/each}
                  </select>
                </form>
              </article>
            {/each}
          </div>
        </div>
      {/each}
    </div>
  {/if}

  <h2>Add a deal</h2>
  <form  method="post"  action="/deals"  class="row wrap"  data-new-deal>
    <input  name="name"  placeholder="Deal name"  required aria-label="Name">
    <select  name="company"  aria-label="Company"><option  value="">No company</option>
      {#each F.rng(dot?.Companies) as [k7, d7]}<option  value={S(d7?.ID)}>{d7?.Name}</option>{/each}</select>
    <input  name="amount"  placeholder="Amount"  style="width:8rem"  aria-label="Amount">
    <select  name="stage"  aria-label="Stage">{#each F.rng(dot?.DealStages) as [k8, d8]}<option  value={S(d8)}>{F.stageName(d8)}</option>{/each}</select>
    <input  name="close"  type="date"  aria-label="Close date">
    {@render ownerselect(F.dict("Name", "owner", "People", dot?.Owners, "Selected", ""))}
    <button  type="submit">Add</button>
  </form>
</section>
{/snippet}
{@render content(dot)}
