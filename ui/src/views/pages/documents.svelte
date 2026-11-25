<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  import Embedded from '../../components/Embedded.svelte';
  import Directory from '../../components/Directory.svelte';
  // The quick view of a task, and the new panel: each is another page of the interface drawn inside a popover when its button is
  // pressed. n makes a second press on the same task fetch again.
  let peek = $state(null);
  let neu = $state(null);
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>
{#snippet controls(dot)}
  
  <form  id="viewform"  method="get"  action={S(dot?.Base)}  hidden>
    {#each F.rng(dot?.Query?.Filters) as [k1, d1]}{@const v_f = d1}<input  type="hidden"  name="f"  value={S(F.filterValue(v_f))}>{/each}
    {#if F.t(F.ne(dot?.ViewKind, "table"))}<input  type="hidden"  name="view"  value={S(dot?.ViewKind)}>{/if}
    {#if F.t(dot?.Query?.Closed)}<input  type="hidden"  name="closed"  value="1">{/if}
  </form>

  <div  class="toolbar row">
    <button  type="button"  class="chip"  popovertarget="pop-filter">Filter</button>
    <div  id="pop-filter"  popover class="popover">
      {#if F.len(dot?.SavedViews)}
        <p  class="muted small">Views</p>
        <nav  class="filters"  data-dept-views>
          {#each F.rng(dot?.SavedViews) as [kv, dv]}
            <span  class="viewchip">
              <a  class={"chip " + S((F.t(F.and(dot?.SavedView, F.eq(dot?.SavedView?.ID, dv?.ID))) ? ("on") : ""))}  href={(S(dv?.Path))}>{dv?.Name}{#if F.t(dv?.Shared) && dv?.OwnerID !== dot?.Me}<span  class="muted"> &middot; shared</span>{/if}</a>
              {#if dv?.OwnerID === dot?.Me}
                <form  class="inline"  method="post"  action={("/views/" + S(dv?.ID) + "/delete")}>
                  <button  type="submit"  class="ghost small"  aria-label={("Remove the view " + S(dv?.Name))}  title="Remove this view">&times;</button>
                </form>
              {/if}
            </span>
          {/each}
        </nav>
      {/if}
      <p  class="muted small">Status</p>
      <nav  class="filters">
        <a  class={"chip " + S((F.t(F.not(F.hasFilter(dot?.Query, F.col("status")))) ? ("on") : ""))}             href={(S(dot?.Base) + S(F.clearStatus(dot?.Query, dot?.ViewKind)))}>All</a>
        {#each F.rng(dot?.Statuses) as [k2, d2]}{@const v_s = d2}
          <a  class={"chip " + S((F.t(F.m(dot?.Query, "Has", F.col("status"), v_s)) ? ("on") : ""))}               href={(S(dot?.Base) + S(F.withStatus(dot?.Query, dot?.ViewKind, v_s)))}>{F.statusLabel(v_s)}</a>
        {/each}
      </nav>
      {#if F.t(dot?.PropDefs)}
        <nav  class="filters">
          {#each F.rng(dot?.PropDefs) as [k3, d3]}{@const v_d = d3}
            {#if F.t(F.eq(v_d?.DataType, "select"))}
              {#each F.rng(v_d?.Options) as [k4, d4]}{@const v_o = d4}
                <a  class={"chip " + S((F.t(F.m(dot?.Query, "Has", F.prop(v_d?.Key), v_o)) ? ("on") : ""))}                     href={(S(dot?.Base) + S(F.toggleProp(dot?.Query, dot?.ViewKind, v_d?.Key, v_o)))}>{v_d?.Label}: {v_o}</a>
              {/each}
            {/if}
          {/each}
        </nav>
      {/if}
    </div>

    <button  type="button"  class="chip"  popovertarget="pop-group">Group by</button>
    <div  id="pop-group"  popover class="popover">
      <label  for="group">Group by</label>
      <select  id="group"  name="group"  {@attach A("form", "viewform")}  data-autosubmit>
        {#each F.rng(dot?.GroupOpts) as [k5, d5]}<option  value={S(d5?.Key)}  {...(F.t(d5?.On) ? {"selected": true} : {})}>{d5?.Label}</option>{/each}
      </select>
      <noscript><button  type="submit"  {@attach A("form", "viewform")}>Apply</button></noscript>
    </div>

    <button  type="button"  class="chip"  popovertarget="pop-cols">Columns</button>
    <div  id="pop-cols"  popover class="popover">
      <label  for="cols">Columns</label>
      <select  id="cols"  name="cols"  multiple size="6"  {@attach A("form", "viewform")}  data-autosubmit>
        {#each F.rng(dot?.ColumnOpts) as [k6, d6]}<option  value={S(d6?.Key)}  {...(F.t(d6?.On) ? {"selected": true} : {})}>{d6?.Label}</option>{/each}
      </select>
      <noscript><button  type="submit"  {@attach A("form", "viewform")}>Apply</button></noscript>
    </div>

    <button  type="button"  class="chip"  popovertarget="pop-sort">Sort</button>
    <div  id="pop-sort"  popover class="popover">
      <label  for="sort">Sort</label>
      <select  id="sort"  name="sort"  {@attach A("form", "viewform")}  data-autosubmit>
        <option  value="">Recently updated</option>
        {#each F.rng(dot?.ColumnOpts) as [k7, d7]}
          <option  value={S(d7?.Key)}  {...(F.t(F.sortedBy(dot?.Query, d7?.Key, false)) ? {"selected": true} : {})}>{d7?.Label} up</option>
          <option  value={S(d7?.Key) + ":desc"}  {...(F.t(F.sortedBy(dot?.Query, d7?.Key, true)) ? {"selected": true} : {})}>{d7?.Label} down</option>
        {/each}
      </select>
      <noscript><button  type="submit"  {@attach A("form", "viewform")}>Apply</button></noscript>
    </div>

    
    <a  class={"chip " + S((F.t(dot?.Query?.Closed) ? ("on") : ""))}         href={(S(dot?.Base) + S(F.toggleClosed(dot?.Query, dot?.ViewKind)))}>Show closed</a>

    <button  type="button"  class="chip"  popovertarget="pop-save">Save view</button>
    <div  id="pop-save"  popover class="popover">
      <form  class="row saveview"  method="post"  action="/views">
        <input  type="hidden"  name="from"  value={S(dot?.SaveTarget)}>
        <input  name="name"  placeholder="Save this list as…"                 value={S((F.t(dot?.SavedView) ? (S(dot?.SavedView?.Name)) : ""))}>
        <label  class="check"><input  type="checkbox"  name="shared"            {...(F.t(F.and(dot?.SavedView, dot?.SavedView?.Shared)) ? {"checked": true} : {})}> Shared</label>
        <button  type="submit">Save view</button>
      </form>
    </div>
  </div>
{/snippet}
{#snippet listbody(dot)}
  {#if F.t(F.eq(dot?.ViewKind, "board"))}{@render board(dot)}
  {:else if F.t(F.eq(dot?.ViewKind, "calendar"))}{@render calendar(dot)}
  {:else if F.t(F.eq(dot?.ViewKind, "timeline"))}{@render timeline(dot)}
  {:else}{@render table(dot)}{/if}
{/snippet}
{#snippet table(dot)}
  {#if F.t(dot?.GroupedTable)}
    
    {#each F.rng(dot?.GroupedTable) as [k8, d8]}
      
      {#if F.t(F.or(d8?.Rows, F.not(F.isClosedStatus(d8?.Value)), dot?.Query?.Closed))}
      
      <details  class="tablegroup"  data-value={S(d8?.Value)}  open>
        <summary>{d8?.Label} <span  class="muted small">{F.len(d8?.Rows)}</span></summary>
        <table  class="list">
          <thead>
            <tr>{#each F.rng(dot?.Columns) as [k9, d9]}<th>{F.fieldLabel(d9, dot?.PropDefs)}</th>{/each}</tr>
          </thead>
          <tbody>
            {#each F.rng(d8?.Rows) as [k10, d10]}{@const v_row = d10}
              
              
              <tr  data-doc={S(v_row?.Doc?.ID)} {...(F.t(v_row?.Depth) ? {"data-child-of": S(v_row?.Parent), "class": "subrow depth" + S(v_row?.Depth), "hidden": true} : {})}>
                {#each F.rng(v_row?.Cells) as [k11, d11]}
                  <td  class={S((F.t(d11?.Muted) ? ("muted") : ""))}>
                    {#if F.t(d11?.Link)}
                      {#if F.t(v_row?.Children)}<button  type="button"  class="ghost small subtoggle"  data-subtasks={S(v_row?.Doc?.ID)}                          aria-expanded="false"  title="Show subtasks">&#9656; {v_row?.Children}</button>{/if}
                      {#if F.t(v_row?.Subtask)}<span  class="tag small"  title="A subtask of something not in this list">subtask</span>{/if}
                      
                      <button  type="button"  class="ghost small peekbtn"  popovertarget="taskpanel" onclick={() => (peek = { url: "/documents/" + S(v_row?.Doc?.ID) + "?peek=1", n: Date.now() })}  title="Quick view"  aria-label={"Quick view of " + S(d11?.Text)}>&#128065;</button>
                      <a  href={("/documents/" + S(v_row?.Doc?.ID))}>{d11?.Text}</a>
                    {:else if F.t(d11?.Class)}<span  class={S(d11?.Class)}>{d11?.Text}</span>
                    {:else}{d11?.Text}{/if}
                  </td>
                {/each}
              </tr>
            {/each}
            
            {#if F.t(F.and(F.eq(dot?.GroupBy, "status"), F.hasTaskFields(dot?.Kind)))}
              <tr  class="addrow">
                <td  colspan={S(F.len(dot?.Columns))}>
                  <form  method="post"  action="/documents"  class="row">
                    <input  type="hidden"  name="kind"  value={S(dot?.Kind)}>
                    <input  type="hidden"  name="status"  value={S(d8?.Value)}>
                    <input  type="hidden"  name="back"  value={S(dot?.CurrentURL)}>
                    <input  name="title"  placeholder="Add a task"  required style="flex:1">
                    <button  type="submit"  class="ghost small">Add</button>
                  </form>
                </td>
              </tr>
            {/if}
          </tbody>
        </table>
      </details>
      {/if}
    {/each}
  {:else}
    <p  class="muted">Nothing matches.</p>
  {/if}
{/snippet}
{#snippet board(dot)}
  
  <div  class="board"  data-group={S(dot?.GroupBy)}>
    {#each F.rng(dot?.Groups) as [k12, d12]}
      <div  class="boardcol"  data-value={S(d12?.Value)}>
        <h3>{d12?.Label} <span  class="muted small">{F.len(d12?.Rows)}</span></h3>
        <div  class="cards"  data-drop>
          {#each F.rng(d12?.Rows) as [k13, d13]}
            <article  class="card2"  draggable="true"  data-id={S(d13?.ID)}>
              <a  href={("/documents/" + S(d13?.ID))}>{#if F.t(d13?.Title)}{d13?.Title}{:else}Untitled{/if}</a>
              <div  class="meta">
                <span  class="tag">{d13?.Kind}</span>
                {#if F.t(d13?.Task)}
                  <span  class={"prio p" + S(d13?.Task?.Priority)}>{F.priority(d13?.Task?.Priority)}</span>
                  {#if F.t(d13?.Task?.DueAt)}<span  class="muted small">{F.date(d13?.Task?.DueAt)}</span>{/if}
                {/if}
              </div>
            </article>
          {/each}
        </div>
      </div>
    {/each}
  </div>
{/snippet}
{#snippet calendar(dot)}
  <div  class="row spread calhead">
    <a  class="chip"  href={(S(dot?.Base) + "?view=calendar&month=" + S(dot?.PrevMonth))}>Previous</a>
    <h3>{F.month(dot?.Month)}</h3>
    <a  class="chip"  href={(S(dot?.Base) + "?view=calendar&month=" + S(dot?.NextMonth))}>Next</a>
  </div>
  <table  class="calendar">
    <thead><tr>
      <th>Mon</th><th>Tue</th><th>Wed</th><th>Thu</th><th>Fri</th><th>Sat</th><th>Sun</th>
    </tr></thead>
    <tbody>
      {#each F.rng(dot?.Weeks) as [k14, d14]}
        <tr>
          {#each F.rng(d14) as [k15, d15]}
            <td  class={S((F.t(F.not(d15?.InMonth)) ? ("outside") : "")) + " " + S((F.t(d15?.Today) ? ("today") : ""))}>
              <span  class="daynum">{F.day(d15?.Date)}</span>
              {#each F.rng(d15?.Rows) as [k16, d16]}
                <a  class={"calitem " + S((F.t(d16?.Task) ? (S(d16?.Task?.Status)) : ""))}                     href={("/documents/" + S(d16?.ID))}>{#if F.t(d16?.Title)}{d16?.Title}{:else}Untitled{/if}</a>
              {/each}
            </td>
          {/each}
        </tr>
      {/each}
    </tbody>
  </table>
  <p  class="muted small">Only things with a due date appear. An undated job is
    not happening on any particular day.</p>
{/snippet}
{#snippet timeline(dot)}
  {#if F.t(dot?.Bars)}
    <div  class="timeline">
      <div  class="row spread muted small">
        <span>{F.date(dot?.TimelineFrom)}</span><span>{F.date(dot?.TimelineTo)}</span>
      </div>
      {#each F.rng(dot?.Bars) as [k17, d17]}
        <div  class="tlrow">
          <a  class="tllabel"  href={("/documents/" + S(d17?.Doc?.ID))}>
            {#if F.t(d17?.Doc?.Title)}{d17?.Doc?.Title}{:else}Untitled{/if}</a>
          <div  class="tltrack">
            <a  class={"tlbar " + S(d17?.Status)}  href={("/documents/" + S(d17?.Doc?.ID))}                 style={"left:" + S(F.pct(d17?.LeftPct)) + ";width:" + S(F.pct(d17?.WidthPct))}></a>
          </div>
        </div>
      {/each}
    </div>
  {:else}
    <p  class="muted">Nothing here has both a start and an end, so there is
      nothing to draw.</p>
  {/if}
{/snippet}

{#snippet content(dot: any)}
<section>
  
  <div  class="row spread pageheader">
    <div  class="row">
      <h1>{#if dot?.SpaceName && !dot?.Kind && !dot?.SavedView}{dot?.SpaceName}{:else}{dot?.Heading}{/if}</h1>
      
      <span  class="muted small">
        {#if F.t(F.lt(dot?.Shown, dot?.Total))}{dot?.Shown} of {dot?.Total}{:else}{dot?.Total}{/if}
      </span>
      {#if dot?.ClosedHidden}<a  class="muted small"  data-closed-hidden  href={(S(dot?.Base) + S(F.toggleClosed(dot?.Query, dot?.ViewKind)))}>{dot?.ClosedHidden} closed</a>{/if}
      <nav  class="viewtabs">
        {#each F.rng(dot?.Tabs) as [k18, d18]}<a  class={"chip " + S((F.t(d18?.On) ? ("on") : ""))}  href={(S(d18?.Href))}>{d18?.Label}</a>{/each}
      </nav>
    </div>
    <button  class="primary"  popovertarget="newpanel" onclick={() => (neu = { url: "/documents/new?kind=" + S(dot?.Kind) + S((((w19) => F.t(w19) ? ("&space=" + S(F.deref_s(w19))) : "")(dot?.Query?.SpaceID))), n: Date.now() })}>New</button>
  </div>

  
  <Directory pg={dot} />

  
  {#if F.t(F.and(dot?.Query?.SpaceID, F.ne(dot?.Query?.Kind, "daily_report")))}
    <section  class="dailyreports">
      <h3  class="row spread">
        <span>Daily reports</span>
        <a  class="ghost small"  href={("/reports/daily/new?space=" + S(F.deref_s(dot?.Query?.SpaceID)))}>New daily report</a>
      </h3>
      {#if F.t(dot?.DailyReports)}
        <ul  class="files">
          {#each F.rng(dot?.DailyReports) as [k21, d21]}
            <li>
              <a  href={("/documents/" + S(d21?.ID))}>{#if F.t(d21?.Title)}{d21?.Title}{:else}Untitled{/if}</a>
              <span  class="muted small">{F.date(d21?.UpdatedAt)}</span>
            </li>
          {/each}
        </ul>
      {:else}
        <p  class="muted small">No daily reports yet.</p>
      {/if}
    </section>
  {:else if F.t(F.and(F.not(dot?.Query?.SpaceID), F.eq(dot?.Query?.Kind, "")))}
    
    <section  class="dailyreports">
      <h3  class="row spread">
        <span>Daily reports</span>
        <form  method="get"  action="/documents"  class="inline row" onchange={(e) => e.currentTarget.requestSubmit()}>
          <label  for="reports"  class="muted small">Department</label>
          <select  id="reports"  name="reports">
            <option  value="">All departments</option>
            {#each F.rng(dot?.ReportSpaces) as [k22, d22]}
              <option  value={S(d22?.ID)}  {...(F.t(F.eq(d22?.ID, dot?.ReportSpace)) ? {"selected": true} : {})}>{d22?.Name}</option>
            {/each}
          </select>
          <noscript><button  type="submit"  class="ghost small">Show</button></noscript>
        </form>
        
        <a  class="ghost small"  href={("/reports/daily/new" + S((F.t(dot?.ReportSpace) ? ("?space=" + S(dot?.ReportSpace)) : "")))}>New daily report</a>
      </h3>
      {#if F.t(dot?.DailyReports)}
        <ul  class="files">
          {#each F.rng(dot?.DailyReports) as [k23, d23]}
            <li>
              <a  href={("/documents/" + S(d23?.ID))}>{#if F.t(d23?.Title)}{d23?.Title}{:else}Untitled{/if}</a>
              <span  class="tag">{#if F.t(d23?.SpaceID)}{@const w24 = d23?.SpaceID}{#if F.t(F.index(dot?.ReportSpaceNames, F.deref_s(w24)))}{@const w25 = F.index(dot?.ReportSpaceNames, F.deref_s(w24))}{w25}{:else}Elsewhere{/if}{:else}No department{/if}</span>
              <span  class="muted small">{F.date(d23?.UpdatedAt)}</span>
            </li>
          {/each}
        </ul>
      {:else}
        <p  class="muted small">No daily reports{#if F.t(dot?.ReportSpace)} in this department{/if} yet.</p>
      {/if}
    </section>
  {/if}

  {@render controls(dot)}

  
  <div  id="newpanel"  popover class="popover popover-lg popover-new"><div  id="panel">{#if neu}{#key neu.n}<Embedded url={neu.url} />{/key}{/if}</div></div>

  <div  id="doclist"  data-live>
    {@render listbody(dot)}
  </div>

  
  <div  id="taskpanel"  popover class="popover popover-xl">
    <div  id="taskpanel-body">{#if peek}{#key peek.n}<Embedded url={peek.url} />{/key}{:else}Loading…{/if}</div>
  </div>
</section>
{/snippet}
{@render content(dot)}
