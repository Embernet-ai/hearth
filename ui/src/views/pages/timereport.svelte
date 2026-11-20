<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  {#if F.t(dot?.ReportPerson)}
    <div  class="row spread">
      <h1>{#if F.t(dot?.ReportPersonName)}{dot?.ReportPersonName}{:else}Time report{/if}</h1>
      <span  class="muted small">{F.len(dot?.Report)} entries · {F.hours(dot?.ReportTotal)} total</span>
    </div>
    <p  class="muted small">
      <a  href={("/reports/time?from=" + S(dot?.ReportFrom) + "&to=" + S(dot?.ReportTo))}>← All people</a>
    </p>
  {:else}
    <div  class="row spread">
      <h1>Time report</h1>
      <span  class="muted small">{F.len(dot?.Totals)} people · {F.hours(dot?.ReportTotal)} total</span>
    </div>
    <p  class="muted small">
      Whole-org total per person for the range below. Click a name for that
      person's entries -- what they logged it against and their own notes.
    </p>
  {/if}

  <form  class="row"  method="get"  action="/reports/time">
    {#if F.t(dot?.ReportPerson)}<input  type="hidden"  name="person"  value={S(dot?.ReportPerson)}>{/if}
    <label  for="from"  class="muted small">From</label>
    <input  id="from"  name="from"  type="date"  value={S(dot?.ReportFrom)}>
    <label  for="to"  class="muted small">to</label>
    <input  id="to"  name="to"  type="date"  value={S(dot?.ReportTo)}>
    <button  type="submit">Show</button>
    <a  class="chip"  href={("/reports/time?" + S((F.t(dot?.ReportPerson) ? ("person=" + S(dot?.ReportPerson) + "&") : "")) + "from=" + S(dot?.ReportFrom) + "&to=" + S(dot?.ReportTo) + "&format=xlsx")}>Download Excel</a>
    <a  class="chip"  href={("/reports/time?" + S((F.t(dot?.ReportPerson) ? ("person=" + S(dot?.ReportPerson) + "&") : "")) + "from=" + S(dot?.ReportFrom) + "&to=" + S(dot?.ReportTo) + "&format=csv")}>Download CSV</a>
  </form>

  {#if F.t(dot?.ReportPerson)}
    {#if F.t(dot?.Report)}
      <table  class="list">
        <thead>
          <tr><th>Date</th><th>Hours</th><th>Space</th><th>Work</th><th>Tags</th><th>Note</th></tr>
        </thead>
        <tbody>
          {#each F.rng(dot?.Report) as [k1, d1]}
            <tr>
              <td  class="muted small">{F.date(d1?.StartedAt)}</td>
              <td>{F.hours(d1?.Minutes)}</td>
              <td  class="muted">{d1?.SpaceName}</td>
              <td  class="muted">{d1?.DocTitle}</td>
              <td  class="muted small">{#each F.rng(d1?.Tags) as [k2, d2]}{d2} {/each}</td>
              <td  class="muted small">{d1?.Note}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p  class="muted">Nothing logged in this range.</p>
    {/if}
  {:else}
    {#if F.t(dot?.Totals)}
      <table  class="list">
        <thead><tr><th>Person</th><th>Hours</th></tr></thead>
        <tbody>
          {#each F.rng(dot?.Totals) as [k3, d3]}
            <tr>
              <td><a  href={("/reports/time?person=" + S(d3?.PrincipalID) + "&from=" + S(dot?.ReportFrom) + "&to=" + S(dot?.ReportTo))}>{d3?.PersonName}</a></td>
              <td>{F.hours(d3?.Minutes)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p  class="muted">Nothing logged in this range.</p>
    {/if}
  {/if}
</section>
{/snippet}
{@render content(dot)}
