<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <div  class="row spread">
    <h1>Maintenance</h1>
    <div  class="row">
      <a  class="chip"  href={("/maintenance?from=" + S(dot?.PrevWeek))}>Earlier</a>
      <span  class="muted small">{F.date(dot?.WeekFrom)} to {F.date(dot?.WeekTo)}</span>
      <a  class="chip"  href={("/maintenance?from=" + S(dot?.NextWeek))}>Later</a>
    </div>
  </div>

  {#if F.t(dot?.Compliance)}{@const w1 = dot?.Compliance}
    <div  class="row wrap"  data-compliance>
      <span  class="chip"  data-compliance-percent={S(w1?.Percent)}>
        {w1?.Percent}% completed</span>
      <span  class="chip"  data-compliance-ontime={S(w1?.OnTimePercent)}>
        {w1?.OnTimePercent}% on time</span>
      
      <span  class="muted small"  data-due={S(w1?.Due)}>{w1?.Due} due</span>
      <span  class="muted small"  data-done-on-time={S(w1?.OnTime)}>{w1?.OnTime} on time</span>
      <span  class="muted small"  data-late={S(w1?.Late)}>{w1?.Late} late</span>
      <span  class={"muted small " + S((F.t(w1?.Missed) ? ("over") : ""))}  data-missed={S(w1?.Missed)}>
        {w1?.Missed} missed</span>
      <span  class="muted small"  data-open={S(w1?.Open)}>{w1?.Open} still open</span>
    </div>
    <p  class="muted small">
      Counted from occurrences rather than from work orders, so renaming a job
      or deleting it does not change what the record says was due. Work that is
      not yet late is left out of the percentage: a job due on Friday is not a
      breach on Wednesday.
    </p>
  {/if}

  <h2>Schedules</h2>
  {#if F.t(dot?.Schedules)}
    <table  class="list">
      <thead>
        <tr>
          <th>Schedule</th><th>Asset</th><th>Interval</th><th>Next due</th>
          <th>Open job</th><th></th>
        </tr>
      </thead>
      <tbody>
        {#each F.rng(dot?.Schedules) as [k2, d2]}
          <tr  data-schedule={S(d2?.ID)}>
            <td>
              {d2?.Name}
              {#if F.t(F.not(d2?.Enabled))}<span  class="tag">paused</span>{/if}
            </td>
            <td><a  href={("/documents/" + S(d2?.AssetID))}>{d2?.AssetTitle}</a></td>
            <td  class="muted small">{F.interval(d2)}</td>
            <td  class={S((F.t(F.and(d2?.Enabled, d2?.Overdue)) ? ("over") : ""))}                  data-due={S(F.m(d2?.DueAt, "Format", "2006-01-02"))}>
              {F.date(d2?.DueAt)}
              {#if F.t(F.and(d2?.Enabled, d2?.Overdue))}<span  class="muted small">overdue</span>{/if}
              {#if F.t(d2?.ProjectedMeterDue)}
                <span  class="muted small">meter {F.date(d2?.ProjectedMeterDue)}</span>
              {/if}
            </td>
            <td>
              {#if F.t(d2?.Open)}
                <a  href={("/documents/" + S(F.deref_s(d2?.Open?.WorkOrderID)))}>{d2?.Open?.WorkOrder}</a>
              {:else}
                <form  method="post"  action={"/maintenance/" + S(d2?.ID) + "/raise"}  class="inline">
                  <button  type="submit"  class="ghost small">Raise now</button>
                </form>
              {/if}
            </td>
            <td>
              <form  method="post"  action={"/maintenance/" + S(d2?.ID) + "/enabled"}  class="inline">
                <input  type="hidden"  name="enabled"  value={S((F.t(d2?.Enabled) ? ("off") : ("on")))}>
                <button  type="submit"  class="ghost small">{#if F.t(d2?.Enabled)}Pause{:else}Resume{/if}</button>
              </form>
              <form  method="post"  action={"/maintenance/" + S(d2?.ID) + "/delete"}  class="inline">
                <button  type="submit"  class="ghost small">Remove</button>
              </form>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">No schedules yet. Add one below, or from an asset.</p>
  {/if}

  <h2>Add a schedule</h2>
  <form  method="post"  action="/maintenance"  class="row wrap">
    <select  name="asset"  required aria-label="Asset">
      <option  value="">Which asset</option>
      {#each F.rng(dot?.Assignable) as [k3, d3]}
        <option  value={S(d3?.ID)}>{#if F.t(d3?.Title)}{d3?.Title}{:else}Untitled{/if}</option>
      {/each}
    </select>
    <input  name="name"  placeholder="Quarterly service"  required aria-label="Schedule name">
    <select  name="template"  aria-label="Procedure">
      <option  value="">No procedure</option>
      {#each F.rng(dot?.Templates) as [k4, d4]}<option  value={S(d4?.ID)}>{d4?.Title}</option>{/each}
    </select>
    <input  name="every_days"  type="number"  min="1"  placeholder="days"             style="width:6rem"  aria-label="Every how many days">
    <input  name="meter_key"  placeholder="run_hours"  style="width:8rem"  aria-label="Meter">
    <input  name="every_units"  type="number"  step="any"  min="1"  placeholder="units"             style="width:6rem"  aria-label="Every how many meter units">
    <input  name="lead_days"  type="number"  min="0"  max="365"  value="7"             style="width:5rem"  aria-label="Days of notice">
    <button  type="submit">Schedule</button>
  </form>
  <p  class="muted small">
    A schedule with a procedure attached raises the whole job, checklist and
    all. Without one it raises a work order with a title, which is a reminder
    rather than an instruction, and the crew already has plenty of those.
  </p>

  {#if F.t(dot?.Compliance)}{@const w5 = dot?.Compliance}
    {#if F.t(w5?.Rows)}
      <h2>What came round</h2>
      <table  class="list">
        <thead>
          <tr><th>Schedule</th><th>Due</th><th>Work order</th><th>Outcome</th></tr>
        </thead>
        <tbody>
          {#each F.rng(w5?.Rows) as [k6, d6]}
            <tr  data-occurrence={S(d6?.ID)}>
              <td>{d6?.Name}</td>
              <td  class="muted small">{F.date(d6?.DueAt)}</td>
              <td>
                {#if F.t(d6?.WorkOrder)}
                  <a  href={("/documents/" + S(F.deref_s(d6?.WorkOrderID)))}>{d6?.WorkOrder}</a>
                {:else}
                  <span  class="muted small">none</span>
                {/if}
              </td>
              <td>
                {#if F.t(d6?.CompletedAt)}
                  <span  class={"status " + S((F.t(d6?.Late) ? ("blocked") : ("done")))}>
                    {#if F.t(d6?.Late)}late{:else}on time{/if}</span>
                  <span  class="muted small">{F.date(d6?.CompletedAt)}</span>
                {:else if F.t(d6?.Missed)}
                  <span  class="status blocked">missed</span>
                {:else}
                  <span  class="status todo">open</span>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  {/if}
</section>
{/snippet}
{@render content(dot)}
