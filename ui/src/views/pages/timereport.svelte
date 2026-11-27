<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);

  // The People selector in the filter bar: who the report covers, on screen and in every file. Nobody picked means everybody.
  // The choices are searched in the page; the boxes stay in the form whether or not the search is showing them, so what is
  // picked is what is sent. The details export reads the same picks, live, so it never goes out for somebody not ticked.
  let whoSel: string[] = $state([...(pg?.ReportWho ?? [])]);
  let whoQuery: string = $state('');
  const whoOptions: any[] = $derived(dot?.ReportWhoOptions ?? []);
  const whoShown = (name: string) => !whoQuery.trim() || String(name).toLowerCase().includes(whoQuery.trim().toLowerCase());
  const allWho: boolean = $derived(whoOptions.length > 0 && whoSel.length === whoOptions.length);
  const whoLabel: string = $derived(
    whoSel.length === 0 ? 'Everyone'
      : whoSel.length === 1 ? String(whoOptions.find((o: any) => String(o.PrincipalID) === whoSel[0])?.PersonName ?? '1 person')
      : `${whoSel.length} people`);
  function whoToggle(id: string, on: boolean) {
    whoSel = on ? [...whoSel.filter((p) => p !== id), id] : whoSel.filter((p) => p !== id);
  }
  function whoSelectAll() {
    const ids = whoOptions.filter((o) => whoShown(o.PersonName)).map((o) => String(o.PrincipalID));
    whoSel = [...new Set([...whoSel, ...ids])];
  }
  // The details export: one file with every entry for the picked people, under the filters as they stand. Everyone picked, or
  // nobody, goes out as all=1 so an organisation's address stays short.
  const detailHref = (fmt: string) => {
    const q = new URLSearchParams();
    q.set('detail', '1'); q.set('from', S(dot?.ReportFrom)); q.set('to', S(dot?.ReportTo));
    if (F.t(dot?.ReportApproval)) q.set('approval', S(dot?.ReportApproval));
    if (F.t(dot?.ReportTeam)) q.set('team', S(dot?.ReportTeam));
    if (F.t(dot?.ReportSpaceID)) q.set('space', S(dot?.ReportSpaceID));
    if (F.t(dot?.ReportTag)) q.set('tag', S(dot?.ReportTag));
    if (whoSel.length === 0 || allWho) q.set('all', '1'); else for (const id of whoSel) q.append('people', id);
    q.set('format', fmt);
    return '/reports/time?' + q.toString();
  };
</script>

{#snippet content(dot: any)}
<section>
  {#if F.t(dot?.ReportPerson)}
    <div  class="row spread">
      <h1>{#if F.t(dot?.ReportPersonName)}{dot?.ReportPersonName}{:else}Time report{/if}</h1>
      <span  class="muted small">{F.len(dot?.Report)} entries · {F.hours(dot?.ReportTotal)} total</span>
    </div>
    <p  class="muted small">
      <a  href={("/reports/time?from=" + S(dot?.ReportFrom) + "&to=" + S(dot?.ReportTo) + S(dot?.ReportFilterQS))}>← All people</a>
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

  <form  class="reportfilters"  method="get"  action="/reports/time">
    {#if F.t(dot?.ReportPerson)}<input  type="hidden"  name="person"  value={S(dot?.ReportPerson)}>{/if}
    <div  class="fld"><label  for="from"  class="muted small">From</label>
    <input  id="from"  name="from"  type="date"  value={S(dot?.ReportFrom)}></div>
    <div  class="fld"><label  for="to"  class="muted small">To</label>
    <input  id="to"  name="to"  type="date"  value={S(dot?.ReportTo)}></div>
    <div  class="fld"><label  for="f-approval"  class="muted small">Approval</label>
    <select  id="f-approval"  name="approval">
      <option  value=""  selected={!F.t(dot?.ReportApproval)}>Any</option>
      <option  value="approved"  selected={S(dot?.ReportApproval) === 'approved'}>Approved</option>
      <option  value="pending"  selected={S(dot?.ReportApproval) === 'pending'}>Pending</option>
      <option  value="rejected"  selected={S(dot?.ReportApproval) === 'rejected'}>Rejected</option>
    </select></div>
    {#if whoOptions.length > 0}
      <div  class="fld"><label  for="f-people">People</label>
        <details  class="whopick"  data-who>
          <summary  id="f-people"  class="whosummary">{whoLabel}</summary>
          <div  class="card whopanel">
            <input  type="search"  placeholder="Search people"  aria-label="Search people"  bind:value={whoQuery}>
            <div  class="row">
              <button  type="button"  data-who-all  onclick={whoSelectAll}>Select all</button>
              <button  type="button"  data-who-clear  onclick={() => (whoSel = [])}>Clear</button>
            </div>
            <div  class="wholist">
              {#each whoOptions as o}
                <label  hidden={!whoShown(o.PersonName)}>
                  <input  type="checkbox"  name="people"  value={S(o.PrincipalID)}  checked={whoSel.includes(String(o.PrincipalID))}
                    onchange={(e) => whoToggle(String(o.PrincipalID), (e.currentTarget as HTMLInputElement).checked)}>
                  {o.PersonName} <span  class="muted small">{F.hours(o.Minutes)}</span>
                </label>
              {/each}
            </div>
          </div>
        </details>
      </div>
    {/if}
    {#if F.t(dot?.ReportTeams)}
      <div  class="fld"><label  for="f-team"  class="muted small">Team</label>
      <select  id="f-team"  name="team">
        <option  value=""  selected={!F.t(dot?.ReportTeam)}>Everyone</option>
        {#each F.rng(dot?.ReportTeams) as [kt, dt]}
          <option  value={S(dt?.ID)}  selected={S(dot?.ReportTeam) === S(dt?.ID)}>{dt?.Name}</option>
        {/each}
      </select></div>
    {/if}
    {#if F.t(dot?.ReportSpaceOptions)}
      <div  class="fld"><label  for="f-space"  class="muted small">Space</label>
      <select  id="f-space"  name="space">
        <option  value=""  selected={!F.t(dot?.ReportSpaceID)}>All spaces</option>
        {#each F.rng(dot?.ReportSpaceOptions) as [ks, ds]}
          <option  value={S(ds?.ID)}  selected={S(dot?.ReportSpaceID) === S(ds?.ID)}>{ds?.Path}</option>
        {/each}
      </select></div>
    {/if}
    {#if F.t(dot?.ReportTagOptions)}
      <div  class="fld"><label  for="f-tag"  class="muted small">Tag</label>
      <select  id="f-tag"  name="tag">
        <option  value=""  selected={!F.t(dot?.ReportTag)}>Any tag</option>
        {#each F.rng(dot?.ReportTagOptions) as [kg, dg]}
          <option  value={S(dg?.Name)}  selected={S(dot?.ReportTag) === S(dg?.Name)}>{dg?.Name}</option>
        {/each}
      </select></div>
    {/if}
    <div  class="fld fld-go"><button  type="submit">Show</button>
    <a  class="chip"  href={("/reports/time?" + S((F.t(dot?.ReportPerson) ? ("person=" + S(dot?.ReportPerson) + "&") : "")) + "from=" + S(dot?.ReportFrom) + "&to=" + S(dot?.ReportTo) + S(dot?.ReportFilterQS) + "&format=xlsx")}>Download Excel</a>
    <a  class="chip"  href={("/reports/time?" + S((F.t(dot?.ReportPerson) ? ("person=" + S(dot?.ReportPerson) + "&") : "")) + "from=" + S(dot?.ReportFrom) + "&to=" + S(dot?.ReportTo) + S(dot?.ReportFilterQS) + "&format=csv")}>Download CSV</a></div>
  </form>

  {#if F.t(dot?.ReportPerson)}
    {#if F.t(dot?.Report)}
      <div  class="table-scroll">
      <table  class="list reportrows">
        <colgroup><col  style="width:10%"><col  style="width:8%"><col  style="width:14%"><col  style="width:14%"><col  style="width:18%"><col  style="width:16%"><col  style="width:8%"><col  style="width:12%"></colgroup>
        <thead>
          <tr><th>Date</th><th>Hours</th><th>Space</th><th>Customer/<wbr>Project</th><th>Service/<wbr>Project Phase</th><th>Task</th><th>Tags</th><th>Approval</th></tr>
        </thead>
        <tbody>
          {#each F.rng(dot?.Report) as [k1, d1]}
            <tr  class={S(d1?.Note) ? "hasnote" : ""}>
              <td  class="muted small">{F.date(d1?.StartedAt)}</td>
              <td>{F.hours(d1?.Minutes)}</td>
              <td  class="muted">{d1?.LevelSpace}</td>
              <td  class="muted">{d1?.LevelCustomer}</td>
              <td  class="muted">{d1?.LevelPhase}</td>
              <td  class="muted">{d1?.DocTitle}</td>
              <td  class="muted small">{#each F.rng(d1?.Tags) as [k2, d2]}{d2} {/each}</td>
              <td  class="small">{#if S(d1?.Approval) === 'approved'}Approved{:else if S(d1?.Approval) === 'rejected'}Rejected{:else}Pending{/if}</td>
            </tr>
            {#if S(d1?.Note)}
              <tr  class="noterow">
                <td  colspan="8"  class="muted small">
                  {#if S(d1?.Note).length > 240}
                    <details  class="note"><summary><span  class="short">{S(d1?.Note).slice(0, 240)}… <u>more</u></span><span  class="full">{d1?.Note} <u>less</u></span></summary></details>
                  {:else}{d1?.Note}{/if}
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
      </div>
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
              <td><a  href={("/reports/time?person=" + S(d3?.PrincipalID) + "&from=" + S(dot?.ReportFrom) + "&to=" + S(dot?.ReportTo) + S(dot?.ReportFilterQS))}>{d3?.PersonName}</a></td>
              <td>{F.hours(d3?.Minutes)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p  class="muted">Nothing logged in this range.</p>
    {/if}

    {#if F.t(dot?.ReportNotice)}<p  class="card small"  data-flash>{dot?.ReportNotice}</p>{/if}
    {#if whoOptions.length > 0}
      <div  class="detail-export"  style="margin-top:1.5rem">
        <h2>Export details</h2>
        <p  class="muted small">
          One file with every entry, not just the totals, for the people picked in People above (nobody picked means everyone in this view),
          {S(dot?.ReportFrom)} to {S(dot?.ReportTo)}. Each row carries the person's
          name and id, and whether the hours are approved, pending or rejected. To change
          the dates or filter by approval, team, space or tag, set them above and press Show.
        </p>
        <div  class="row">
          <a  class="chip"  data-detail="csv"  href={detailHref('csv')}>Download details CSV</a>
          <a  class="chip"  data-detail="xlsx"  href={detailHref('xlsx')}>Download details Excel</a>
          <span  class="muted small">{#if whoSel.length === 0}Everyone in this view ({whoOptions.length}){:else}{whoSel.length} of {whoOptions.length} chosen{/if}</span>
        </div>
      </div>
    {/if}
  {/if}
</section>
{/snippet}
{@render content(dot)}
