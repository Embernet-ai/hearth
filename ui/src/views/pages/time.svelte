<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  import { hoursmins } from './shared.svelte';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
  // Whose hours these are: yours, or a colleague's read only (the dashboard's numbers link here).
  const own = $derived(!pg?.TimeOf);
  const keep = $derived((pg?.TimeMonth ? '&span=month' : '') + (pg?.TimeOf ? '&person=' + encodeURIComponent(pg.TimeOf.ID) : ''));
</script>

{#snippet content(dot: any)}
<section>
  <h1>{own ? 'My time' : `${dot?.TimeOf?.Name}'s time`}</h1>
  {#if own}
  <p  class="muted small">
    Your own hours. A job is optional: attach one when there is an obvious one
    and leave it alone when there is not, because an hour filed under the wrong
    task is worse than an hour filed under nothing.
  </p>
  {:else}
  <p  class="muted small">
    {dot?.TimeOf?.Name}'s hours, read only. <a href="/time">Back to my own</a>.
  </p>
  {/if}

  {#if F.t(dot?.JustRemoved)}
    
    <p  class="warn row spread">
      <span>Removed an entry.</span>
      </p><form  method="post"  action={"/time/entries/" + S(dot?.JustRemoved) + "/restore"}  class="inline">
        <input  type="hidden"  name="back"  value={S(dot?.MyTimeBack)}>
        <button  type="submit"  class="ghost small">Undo</button>
      </form>
    <p></p>
  {/if}

  
  <div  class="row spread">
    <div  class="row">
      <a  class="chip"  href={("/time?from=" + S(dot?.PrevWeek) + keep + S((F.t(dot?.ShowRemoved) ? ("&removed=1") : "")))}>Previous</a>
      <span  class="muted">{F.date(dot?.WeekFrom)} to {F.date(dot?.WeekTo)}</span>
      <a  class="chip"  href={("/time?from=" + S(dot?.NextWeek) + keep + S((F.t(dot?.ShowRemoved) ? ("&removed=1") : "")))}>Next</a>
      
      <a  class={"chip" + S((F.t(dot?.ShowRemoved) ? (" on") : ""))}           href={("/time?from=" + S(F.m(dot?.WeekFrom, "Format", "2006-01-02")) + keep + S((F.t(F.not(dot?.ShowRemoved)) ? ("&removed=1") : "")))}>Show removed</a>
    </div>
    <div  class="row">
      <span><strong>{F.hours(dot?.MyMinutes)}</strong> <span  class="muted small">{dot?.TimeMonth ? "this month" : "this week"}</span></span>
      {#if F.t(dot?.MyPending)}
        <span  class="tag">{F.hours(dot?.MyPending)} waiting</span>
      {/if}
    </div>
  </div>

  
  {#if own}
  <form  method="post"  action="/time"  class="stack card">
    <input  type="hidden"  name="week"  value={S(F.m(dot?.WeekFrom, "Format", "2006-01-02"))}>
    <div  class="row">
      <span>How long</span>
      {@render hoursmins("hours", "mins", dot?.TimeForm?.Hours, dot?.TimeForm?.Mins, dot?.TimeForm?.Error, "minutes-error")}

      <label  for="on">When</label>
      <input  id="on"  name="on"  type="date"  value={S(F.m(dot?.WeekFrom, "Format", "2006-01-02"))}               style="width:9.5rem"  aria-label="When">
    </div>

    <div  class="row">
      <label  for="document">Against</label>
      <select  id="document"  name="document"  aria-label="Against which job">
        <option  value="">No job, just my time</option>
        {#each F.rng(dot?.MyOpenWork) as [k1, d1]}
          <option  value={S(d1?.ID)}>{d1?.Title}</option>
        {/each}
      </select>
    </div>

    <div  class="row">
      <input  name="note"  placeholder="What you were doing"  aria-label="Note"  style="flex:1">
      <button  type="submit">Record</button>
    </div>

    {#if F.t(dot?.Tags)}
      <div  class="row"  style="flex-wrap:wrap">
        <span  class="muted small">Tags:</span>
        {#each F.rng(dot?.Tags) as [k2, d2]}
          <label  class="chip"><input  type="checkbox"  name="tags"  value={S(d2?.ID)}> {d2?.Name}</label>
        {/each}
      </div>
    {/if}
  </form>
  {/if}

  {#if own && F.t(dot?.Tags)}
    
    <form  method="post"  action="/time/batch-tag"  class="row">
      <span  class="muted small">Tag this whole week:</span>
      <input  type="hidden"  name="from"  value={S(F.m(dot?.WeekFrom, "Format", "2006-01-02"))}>
      <input  type="hidden"  name="back"  value={S(dot?.MyTimeBack)}>
      <select  name="tag"  required aria-label="Which tag">
        {#each F.rng(dot?.Tags) as [k3, d3]}<option  value={S(d3?.ID)}>{d3?.Name}</option>{/each}
      </select>
      <button  type="submit"  class="ghost small">Apply to {F.date(dot?.WeekFrom)}&ndash;{F.date(dot?.WeekTo)}</button>
    </form>
  {/if}

  {#if own && F.t(F.not(dot?.Timer))}
    <form  method="post"  action="/time/start"  class="row">
      <input  type="hidden"  name="week"  value={S(F.m(dot?.WeekFrom, "Format", "2006-01-02"))}>
      <select  name="document"  aria-label="Start a timer against">
        <option  value="">No job, just my time</option>
        {#each F.rng(dot?.MyOpenWork) as [k4, d4]}
          <option  value={S(d4?.ID)}>{d4?.Title}</option>
        {/each}
      </select>
      <input  name="note"  placeholder="What you are starting"  aria-label="Note">
      <button  type="submit"  class="ghost small">Start a timer</button>
    </form>
  {/if}

  {#if F.t(dot?.MyTime)}
    <table  class="list">
      <thead>
        <tr><th>Day</th><th>How long</th><th>Against</th><th>Note</th><th>State</th><th></th></tr>
      </thead>
      <tbody>
        {#each F.rng(dot?.MyTime) as [k5, d5]}
          <tr {...(F.t(d5?.Deleted) ? {"class": "muted"} : {})}>
            <td>{F.date(d5?.StartedAt)}</td>
            <td>{#if F.t(d5?.Running)}<span  class="tag">running</span>{:else}{F.hours(d5?.Elapsed)}{/if}</td>
            <td>
              {#if F.t(d5?.Unattached)}
                <span  class="muted">&mdash;</span>
              {:else}
                <a  href={("/documents/" + S(d5?.DocumentID))}>{#if F.t(d5?.DocTitle)}{d5?.DocTitle}{:else}a job{/if}</a>
              {/if}
            </td>
            <td  class="muted">{d5?.Note}</td>
            <td>
              
              {#if F.t(d5?.Deleted)}
                <span  class="tag over">removed</span>
              {:else if F.t(d5?.Running)}
                <span  class="muted">still going</span>
              {:else if F.t(d5?.WasApproved)}
                <span  class="tag">approved{#if F.t(d5?.DeciderName)} by {d5?.DeciderName}{/if}</span>
              {:else if F.t(d5?.SentBack)}
                <span  class="tag over">sent back{#if F.t(d5?.DeciderName)} by {d5?.DeciderName}{/if}</span>
                {#if F.t(d5?.DecidedNote)}<div  class="muted small">{d5?.DecidedNote}</div>{/if}
              {:else}
                <span  class="muted">waiting</span>
              {/if}
            </td>
            <td>
              {#if !own}{:else if F.t(d5?.Deleted)}
                <form  method="post"  action={"/time/entries/" + S(d5?.ID) + "/restore"}  class="inline">
                  <input  type="hidden"  name="back"  value={S(dot?.MyTimeBack)}>
                  <button  type="submit"  class="ghost small">Restore</button>
                </form>
              {:else if F.t(F.not(d5?.Running))}{@const v_entryID = d5?.ID}
                
                <form  method="post"  action={"/time/entries/" + S(d5?.ID)}  class="inline row">
                  <input  type="hidden"  name="back"  value={S(dot?.MyTimeBack)}>
                  {@render hoursmins("hours", "mins", dot?.TimeForm?.Fix?.ID === d5?.ID ? dot?.TimeForm?.Fix?.Hours : Math.floor((d5?.Minutes ?? 0) / 60), dot?.TimeForm?.Fix?.ID === d5?.ID ? dot?.TimeForm?.Fix?.Mins : (d5?.Minutes ?? 0) % 60, dot?.TimeForm?.Fix?.ID === d5?.ID ? dot?.TimeForm?.Fix?.Error : '', "fix-error-" + d5?.ID)}
                  <input  name="on"  type="date"  value={S(F.dateinput(d5?.StartedAt))}  style="width:9.5rem"                           aria-label="Corrected day">
                  <button  type="submit"  class="ghost small">Correct</button>
                </form>
                <form  method="post"  action={"/time/entries/" + S(d5?.ID) + "/delete"}  class="inline">
                  <input  type="hidden"  name="back"  value={S(dot?.MyTimeBack)}>
                  <button  type="submit"  class="ghost small">Remove</button>
                </form>
                
                
                <span  class="row"  style="flex-wrap:wrap">
                  {#each F.rng(d5?.Tags) as [k6, d6]}
                    <form  method="post"  action={"/time/entries/" + S(v_entryID) + "/tags/" + S(d6?.ID) + "/delete"}  class="inline">
                      <input  type="hidden"  name="back"  value={S(dot?.MyTimeBack)}>
                      <button  type="submit"  class="tag"  title="Remove tag">{d6?.Name} ×</button>
                    </form>
                  {/each}
                  {#if F.t(dot?.Tags)}
                    <form  method="post"  action={"/time/entries/" + S(v_entryID) + "/tags"}  class="inline">
                      <input  type="hidden"  name="back"  value={S(dot?.MyTimeBack)}>
                      <select  name="tag"  aria-label="Add a tag"  data-autosubmit>
                        <option  value="">+ tag</option>
                        {#each F.rng(dot?.Tags) as [k7, d7]}<option  value={S(d7?.ID)}>{d7?.Name}</option>{/each}
                      </select>
                    </form>
                  {/if}
                </span>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">Nothing recorded {dot?.TimeMonth ? "this month" : "this week"}.</p>
  {/if}
</section>
{/snippet}
{@render content(dot)}
