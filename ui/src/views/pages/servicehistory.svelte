<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Service history</h1>
  <p  class="muted small">
    One customer's work, day by day: each work order and task, what was done,
    who it was assigned to, every comment with its time, the status, and the
    hours logged. Pick the customer, choose dates if you want a range, then
    download the PDF to send them or the CSV for a spreadsheet. You see only
    what your account can open.
  </p>

  <form  method="get"  action="/reports/service"  class="row card">
    <label  for="sh-space"  class="muted small">Customer</label>
    <select  id="sh-space"  name="space"  required>
      <option  value="">Choose a customer or department</option>
      {#each F.rng(dot?.RepTree) as [k1, d1]}
        <option  value={S(d1?.ID)}  {...(F.t(F.eq(d1?.ID, dot?.RepSpace)) ? {"selected": true} : {})}>{d1?.Path}</option>
      {/each}
    </select>
    <label  for="sh-from"  class="muted small">From</label>
    <input  id="sh-from"  type="date"  name="from"  value={S(dot?.RepFrom)}>
    <label  for="sh-to"  class="muted small">To</label>
    <input  id="sh-to"  type="date"  name="to"  value={S(dot?.RepTo)}>
    <button  type="submit">Show</button>
  </form>

  {#if F.t(dot?.RepSpace)}
    <div  class="card">
      <p><strong>{dot?.SvcRecords}</strong> record{#if F.t(F.ne(dot?.SvcRecords, 1))}s{/if},
        <strong>{dot?.SvcEvents}</strong> event{#if F.t(F.ne(dot?.SvcEvents, 1))}s{/if} on
        <strong>{dot?.SvcDays}</strong> day{#if F.t(F.ne(dot?.SvcDays, 1))}s{/if}.</p>
      {#if F.t(dot?.SvcRecords)}
        <p  class="row">
          <a  class="primary"  href={(S(dot?.SvcPDF))}  download>Download PDF</a>
          <a  class="ghost"  href={(S(dot?.SvcCSV))}  download>Download CSV</a>
        </p>
      {:else}
        <p  class="muted small">Nothing in that range. Clear the dates to see everything.</p>
      {/if}
    </div>
    {#if F.t(dot?.SvcShown)}
      <ul  class="files">
        {#each F.rng(dot?.SvcShown) as [k2, d2]}
          <li><a  href={("/documents/" + S(d2?.ID))}>{d2?.Title}</a>
            <span  class="tag">{d2?.Job}</span>
            {#if F.t(d2?.Status)}<span  class="muted small">{d2?.Status}</span>{/if}
            <span  class="muted small">{F.len(d2?.Events)} event{#if F.t(F.ne(F.len(d2?.Events), 1))}s{/if}</span></li>
        {/each}
      </ul>
    {/if}
  {/if}
</section>
{/snippet}
{@render content(dot)}
