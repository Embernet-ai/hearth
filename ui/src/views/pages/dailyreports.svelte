<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1  class="row spread">
    <span>Daily reports</span>
    {#if F.t(dot?.RepMineToday)}
      <a  class="ghost"  href={("/documents/" + S(dot?.RepMineToday))}>Open my report for today</a>
    {:else}
      <a  class="primary"  href={("/reports/daily/new")}>File today's report</a>
    {/if}
  </h1>

  <form  method="get"  action="/reports/daily"  class="row card">
    <label  for="rr-space"  class="muted small">Department</label>
    <select  id="rr-space"  name="space">
      <option  value="">All departments</option>
      {#each F.rng(dot?.RepTree) as [k1, d1]}
        <option  value={S(d1?.ID)}  {...(F.t(F.eq(d1?.ID, dot?.RepSpace)) ? {"selected": true} : {})}>{d1?.Path}</option>
      {/each}
    </select>
    <label  for="rr-person"  class="muted small">Person</label>
    <select  id="rr-person"  name="person">
      <option  value="">Everyone</option>
      {#each F.rng(dot?.RepPeople) as [k2, d2]}
        <option  value={S(d2?.ID)}  {...(F.t(F.eq(d2?.ID, dot?.RepPerson)) ? {"selected": true} : {})}>{d2?.Name}</option>
      {/each}
    </select>
    <label  for="rr-from"  class="muted small">From</label>
    <input  id="rr-from"  type="date"  name="from"  value={S(dot?.RepFrom)}>
    <label  for="rr-to"  class="muted small">To</label>
    <input  id="rr-to"  type="date"  name="to"  value={S(dot?.RepTo)}>
    <button  type="submit">Show</button>
    <a  class="ghost"  href={(S(dot?.RepPDFHref))}  download>Download PDF</a>
    <a  class="ghost"  href={("/reports/service")}>Service history by customer</a>
  </form>

  <p  class="muted small">{F.len(dot?.RepEntries)} report{#if F.t(F.ne(F.len(dot?.RepEntries), 1))}s{/if}, {dot?.RepFrom} to {dot?.RepTo}. You see the reports you have access to.</p>

  {#each F.rng(dot?.RepEntries) as [k3, d3]}{@const v_e = d3}
    <article  class="card">
      <h3  class="row spread">
        <span><a  href={("/documents/" + S(d3?.ID))}>{d3?.Day}</a> · {d3?.Author}</span>
        <span>
          <span  class="tag">{d3?.SpacePath}</span>
          <a  class="ghost small"  href={("/documents/" + S(d3?.ID) + "/report.pdf")}  download>PDF</a>
        </span>
      </h3>
      {#if F.t(d3?.BodyHTML)}<div  class="prose">{@html d3?.BodyHTML}</div>{:else}<p  class="muted small">No text.</p>{/if}
      {#if F.t(d3?.Photos)}
        <p  class="muted small">Photos</p>
        <ul  class="files">
          {#each F.rng(d3?.Photos) as [k4, d4]}
            <li><a  href={("/documents/" + S(v_e?.ID) + "/files/" + S(d4?.FileID))}>{d4?.Filename}</a></li>
          {/each}
        </ul>
      {/if}
      {#if F.t(d3?.Files)}
        <p  class="muted small">Files</p>
        <ul  class="files">
          {#each F.rng(d3?.Files) as [k5, d5]}<li>{d5?.Filename}</li>{/each}
        </ul>
      {/if}
      {#if F.t(d3?.Comments)}
        <p  class="muted small">{F.len(d3?.Comments)} comment{#if F.t(F.ne(F.len(d3?.Comments), 1))}s{/if}</p>
        <ul  class="plain">
          {#each F.rng(d3?.Comments) as [k6, d6]}<li><strong>{d6?.AuthorName}</strong> {d6?.Body}</li>{/each}
        </ul>
      {/if}
    </article>
  {:else}
    <p  class="muted">No reports in this range.</p>
  {/each}
</section>
{/snippet}
{@render content(dot)}
