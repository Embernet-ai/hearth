<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  import FilePicker from '../../components/FilePicker.svelte';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section  class="card stack">
  <h1>Import</h1>

  {#if F.t(dot?.Import)}
    <div  class="result">
      <p>
        <span  data-created={S(dot?.Import?.Created)}><strong>{dot?.Import?.Created}</strong> created</span> ·
        <span  data-updated={S(dot?.Import?.Updated)}><strong>{dot?.Import?.Updated}</strong> updated</span> ·
        <span  data-failed={S(dot?.Import?.Failed)}><strong>{dot?.Import?.Failed}</strong> failed</span>
        <span  class="muted"  data-total={S(dot?.Import?.Total)}>out of {dot?.Import?.Total}</span>
      </p>
      <p  class="muted small">
        Running the same file again updates what it already brought in rather than
        making a second copy, so a partial import is safe to retry.
      </p>
      {#if F.t(dot?.Import?.Problems)}
        <ul  class="problems">
          {#each F.rng(dot?.Import?.Problems) as [k1, d1]}<li  class="small">{d1}</li>{/each}
        </ul>
      {/if}
      <p><a  href={(S((F.t(F.eq(dot?.Import?.Kind, "work_order")) ? ("/work-orders") : ("/tasks"))))}>See what came in</a></p>
    </div>
  {/if}

  {#if F.t(dot?.Message)}<p  class="error">{dot?.Message}</p>{/if}

  <form  method="post"  action="/import/clickup"  enctype="multipart/form-data"  class="stack">
    <label  for="file">Export file</label>
    <FilePicker  id="file"  name="file"  accept=".csv,.json,text/csv,application/json"  required  kind="ClickUp export file" />
    <p  class="muted small">
      Either format ClickUp gives you. The CSV from a list view, or the JSON from
      the API or a backup.
    </p>

    <label  for="kind">Bring them in as</label>
    <select  id="kind"  name="kind">
      <option  value="task">Tasks</option>
      <option  value="work_order">Work orders</option>
    </select>
    <p  class="muted small">
      ClickUp calls everything a task. If these were maintenance jobs pretending
      to be tasks, they are work orders here.
    </p>

    <div  class="row">
      <button  type="submit"  class="primary">Import</button>
    </div>
  </form>

  <h3>What comes across</h3>
  <ul  class="small muted">
    <li>Name, description, status, priority, and due date.</li>
    <li>Sub-tasks become links between the parent and the child.</li>
    <li>
      Assignees are recorded as text, not matched to people here. A name in
      another system is not proof of who someone is, and guessing assigns work to
      the wrong person silently.
    </li>
    <li>
      Custom statuses are mapped by meaning, so "in review" becomes in progress
      and "won't do" becomes cancelled. Anything unrecognised becomes to do
      rather than being dropped.
    </li>
  </ul>
</section>
{/snippet}
{@render content(dot)}
