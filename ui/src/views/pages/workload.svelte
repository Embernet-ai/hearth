<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <div  class="row spread">
    <h1>Workload</h1>
    <div  class="row">
      <a  class="chip"  href={("/workload?from=" + S(dot?.PrevWeek))}>Previous week</a>
      <span  class="muted small">{F.date(dot?.WeekFrom)} to {F.date(dot?.WeekTo)}</span>
      <a  class="chip"  href={("/workload?from=" + S(dot?.NextWeek))}>Next week</a>
    </div>
  </div>
  <p  class="muted small">
    Estimated minutes of open work due this week, against the hours each person
    has. Completed and cancelled jobs are excluded: counting a cancelled work
    order towards somebody's week is how a planner concludes a person is full
    when they are free.
  </p>

  {#if F.t(dot?.Load)}
    <table  class="list">
      <thead>
        <tr>
          <th>Person</th><th>Open</th><th>Assigned</th><th>Capacity</th>
          <th>Load</th><th>Logged</th>{#if F.t(dot?.IsAdmin)}<th>Set</th>{/if}
        </tr>
      </thead>
      <tbody>
        {#each F.rng(dot?.Load) as [k1, d1]}
          <tr  data-load={S(d1?.PrincipalID)}>
            <td>{d1?.Name}</td>
            <td  class="muted">{d1?.Open}</td>
            <td>{F.hours(d1?.Assigned)}</td>
            <td  class="muted">{F.hours(d1?.Capacity)}</td>
            <td>
              
              <span  class={"loadbar " + S((F.t(F.gt(d1?.Percent, 100)) ? ("over") : ""))}>
                <span  style={"width:" + S(F.pct(F.capped(d1?.Percent)))}></span>
              </span>
              <span  class="muted small">{d1?.Percent}%</span>
            </td>
            <td  class="muted">{F.hours(d1?.Logged)}</td>
            {#if F.t(dot?.IsAdmin)}
              <td>
                <form  method="post"  action={"/people/" + S(d1?.PrincipalID) + "/capacity"}  class="row">
                  <input  name="hours"  type="number"  min="0"  max="168"  style="width:5rem"                           value={S(F.div(d1?.Capacity, 60))}  aria-label="Hours a week">
                  <input  name="rate"  placeholder="rate"                           value={S(F.money(d1?.RateCents))}  style="width:6rem"  aria-label="Hourly rate">
                  <button  type="submit"  class="ghost small">Save</button>
                </form>
              </td>
            {/if}
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">Nobody here yet.</p>
  {/if}
</section>
{/snippet}
{@render content(dot)}
