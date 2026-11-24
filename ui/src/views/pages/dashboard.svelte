<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  import { hoursmins } from './shared.svelte';
  import Stats from '../../components/Stats.svelte';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section  class="dash">

  
  <div  class="card dailycard">
    <h3>My daily report</h3>
    {#if F.t(dot?.Dash?.ReportID)}
      <p  class="small">Filed for {dot?.Dash?.ReportDay}.
        <a  href={("/documents/" + S(dot?.Dash?.ReportID))}>Open it</a> to add a photo or a comment.
        <a  href={("/documents/" + S(dot?.Dash?.ReportID) + "/report.pdf")}  download>PDF</a></p>
    {:else}
      <p  class="small">Not filed yet for {dot?.Dash?.ReportDay}.</p>
      <a  class="button primary small"  href={("/reports/daily/new")}>File today's report</a>
    {/if}
    <a  class="button ghost small"  href={("/reports/daily")}>All daily reports</a>
  </div>

  
  <div  class="weekwrap">
    <div  class="weekhead">
      <h2>Week of {dot?.Dash?.WeekTitle}</h2>
      <span  class="weeknav">
        <a  class="chip"  href={S(dot?.Dash?.PrevWeek)}>Earlier</a>
        <a  class="chip"  href={S(dot?.Dash?.Today)}>This week</a>
        <a  class="chip"  href={S(dot?.Dash?.NextWeek)}>Later</a>
      </span>
    </div>
    <div  class="weekstrip">
      {#each (dot?.Dash?.Week ?? []) as d1}
        <section  class={"weekday" + S((F.t(d1?.Today) ? (" today") : "")) + S((F.t(d1?.Selected) ? (" picked") : "")) + S((F.t(d1?.Weekend) ? (" weekend") : ""))}>
          <a  class="whead"  href={S(d1?.Href)}  aria-label={S(d1?.Weekday) + " " + S(d1?.Label)}>
            <span  class="wd">{d1?.Weekday}</span>
            <span  class="wdate">{d1?.Label}</span>
            {#if F.t(d1?.Count)}<span  class="wcount">{d1?.Count}</span>{/if}
          </a>
          {#if F.t(d1?.Events)}
            <ul  class="plain wlist">
              {#each F.rng(d1?.Events) as [k2, d2]}
                <li><span  class="wtime">{#if F.t(d2?.AllDay)}All day{:else}{F.timeofday(d2?.StartsAt)}{/if}</span>
                  <span  class="wtitle">{d2?.Title}</span></li>
              {/each}
            </ul>
          {/if}
          {#if F.t(d1?.Items)}
            <ul  class="plain wlist">
              {#each F.rng(d1?.Items) as [k3, d3]}
                <li><span  class="wtime">Due</span>
                  <span  class="wtitle"><a  href={("/documents/" + S(d3?.ID))}>{d3?.Title}</a>
                  {#if F.t(d3?.Task)}<span  class={"status " + S(d3?.Task?.Status)}>{F.statusLabel(d3?.Task?.Status)}</span>{/if}</span></li>
              {/each}
            </ul>
          {/if}
          {#if F.t(F.not(F.or(d1?.Events, d1?.Items)))}<p  class="muted small wnone">Nothing on</p>{/if}
        </section>
      {/each}
    </div>
  </div>

  <div  class="dashcols">
    <div  class="colmain">
      <Stats stats={dot?.Dash?.Stats} />
    </div>
    <div  class="colside">
      <div  class="colwork dashwork">
      <h2>Assigned to me</h2>
      {#if F.t(dot?.Dash?.Mine)}
        <div  class="assignedscroll"><table  class="list">
          <thead><tr><th>Title</th><th>Status</th><th>Due</th></tr></thead>
          <tbody>
            {#each F.rng(dot?.Dash?.Mine) as [k5, d5]}
              <tr>
                <td><a  href={("/documents/" + S(d5?.ID))}>{d5?.Title}</a></td>
                <td>{#if F.t(d5?.Task)}<span  class={"status " + S(d5?.Task?.Status)}>{F.statusLabel(d5?.Task?.Status)}</span>{/if}</td>
                <td  class="muted small">{#if F.t(F.and(d5?.Task, d5?.Task?.DueAt))}{F.date(d5?.Task?.DueAt)}{/if}</td>
              </tr>
            {/each}
          </tbody>
        </table></div>
      {:else}
        <div  class="empty">
          <strong>Nothing is assigned to you</strong>
          Work assigned to you appears here, soonest first.
        </div>
      {/if}
      <h2>What's on <a  class="chip"  href={("/calendar")}>Open the calendar</a></h2>
      {#if F.t(dot?.Dash?.Upcoming)}
        <table  class="list">
          <thead><tr><th>When</th><th>What</th></tr></thead>
          <tbody>
            {#each F.rng(dot?.Dash?.Upcoming) as [k6, d6]}
              <tr>
                <td  class="muted">{F.date(d6?.StartsAt)}{#if F.t(F.not(d6?.AllDay))} {F.timeofday(d6?.StartsAt)}{/if}</td>
                <td>
                  {#if F.t(d6?.DocumentID)}
                    <a  href={("/documents/" + S(d6?.DocumentID))}>{d6?.Title}</a>
                    <span  class="muted small">due</span>
                  {:else if F.t(d6?.Link)}
                    <a  href={(S(d6?.Link))}>{d6?.Title}</a>
                    <span  class="muted small">Google</span>
                  {:else}
                    {d6?.Title}
                    {#if F.t(d6?.Place)}<span  class="muted small">{d6?.Place}</span>{/if}
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      {:else}
        <p  class="muted">
          Nothing in the next fortnight.
          <a  href={("/calendar")}>Add something to the calendar</a>.
        </p>
      {/if}
      </div>
      <div  class="colextra">
      <div  class="monthcard">
        {#if dot?.Dash?.Month}
          <h2>{dot?.Dash?.Month?.Title}
            <span  class="weeknav">
              <a  class="chip"  href={S(dot?.Dash?.Month?.Prev)}  aria-label="Earlier month">&lsaquo;</a>
              <a  class="chip"  href={S(dot?.Dash?.Today)}>Today</a>
              <a  class="chip"  href={S(dot?.Dash?.Month?.Next)}  aria-label="Later month">&rsaquo;</a>
            </span></h2>
          <div  class="monthgrid"  role="grid"  aria-label={"Calendar for " + S(dot?.Dash?.Month?.Title)}>
            {#each (dot?.Dash?.Month?.Names ?? []) as n}<span  class="mname"  role="columnheader">{n}</span>{/each}
            {#each (dot?.Dash?.Month?.Weeks ?? []) as wk}
              {#each wk as c}
                <a  role="gridcell"  href={S(c?.Href)}  class={"mday" + S((F.t(c?.InMonth) ? "" : " other")) + S((F.t(c?.Today) ? " today" : "")) + S((F.t(c?.Selected) ? " picked" : "")) + S((F.t(c?.Weekend) ? " weekend" : ""))}
                  title={F.t(c?.Count) ? (S(c?.Count) + " on: " + (c?.Titles ?? []).join(", ")) : "Nothing on"}
                  aria-label={S(c?.Day) + (F.t(c?.Count) ? ", " + S(c?.Count) + " on" : "")}>
                  <span  class="mnum">{c?.Day}</span>
                  {#if F.t(c?.Count)}<span  class="mdots">{#each Array(Math.min(c?.Count, 3)) as _}<i></i>{/each}</span>{/if}
                </a>
              {/each}
            {/each}
          </div>
          <p  class="muted small mfoot"><a  href={("/calendar")}>Open the calendar</a> to add or change events.</p>
        {/if}
      </div>

      <div  class="dashtime">
      
      <h2>My time <a  class="chip"  href={("/time")}>All of it</a></h2>
      {#if F.t(dot?.Timer)}
        <p  class="muted small">
          A timer is running{#if F.t(dot?.Timer?.DocTitle)} on {dot?.Timer?.DocTitle}{/if}.
          Stop it from the bar at the top.
        </p>
      {:else}
        <form  method="post"  action="/time"  class="stack card">
          <div  class="row">
            {@render hoursmins("hours", "mins", "", "", "", "dash-minutes-error")}
            <input  name="on"  type="date"  value={S(dot?.Today)}  aria-label="When">
          </div>
          <select  name="document"  aria-label="Against which job">
            <option  value="">No job, just my time</option>
            {#each F.rng(dot?.Dash?.Mine) as [k7, d7]}<option  value={S(d7?.ID)}>{d7?.Title}</option>{/each}
          </select>
          <input  name="note"  placeholder="What you were doing"  aria-label="Note">
          <div  class="row"><button  type="submit">Record</button></div>
        </form>
        <form  method="post"  action="/time/start"  class="row">
          <select  name="document"  aria-label="Start a timer against">
            <option  value="">No job</option>
            {#each F.rng(dot?.Dash?.Mine) as [k8, d8]}<option  value={S(d8?.ID)}>{d8?.Title}</option>{/each}
          </select>
          <button  type="submit"  class="ghost small">Start a timer</button>
        </form>
      {/if}
      {#if F.t(dot?.MyPending)}
        <p  class="muted small">{F.hours(dot?.MyPending)} waiting on approval.</p>
      {/if}

    </div>
    <div  class="dashfollow">
      <h2>Things you follow</h2>
      {#if F.t(dot?.Dash?.Watching)}
        <ul  class="plain watchlist">
          {#each F.rng(dot?.Dash?.Watching) as [k9, d9]}
            <li>
              <a  href={("/documents/" + S(d9?.ID))}>{d9?.Title}</a>
              <span  class="muted small">{F.date(d9?.UpdatedAt)}</span>
            </li>
          {/each}
        </ul>
      {:else}
        <div  class="empty">
          <strong>You are not following anything</strong>
          Follow a job to hear about it without being assigned to it.
        </div>
      {/if}
    </div>
      </div>
    </div>
  </div>
</section>
{/snippet}
{@render content(dot)}
