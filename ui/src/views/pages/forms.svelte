<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
  const v = $derived(dot?.FormsArea);
  const cell = (x) => (Array.isArray(x) ? x.join(', ') : x === null || x === undefined ? '' : String(x));
</script>

{#snippet content(dot: any)}
<section>
  <h1>Forms</h1>
  {#if F.t(v?.Notice)}<p  class="card small"  data-flash>{v?.Notice}</p>{/if}

  {#if !v?.Configured}
    <p  class="muted"  data-not-set-up>
      Forms is not connected on this installation. Set <code>HEARTH_FORMS_URL</code> and <code>HEARTH_FORMS_API_KEY</code> to the
      address and a read and write API key of your forms app, and your forms and their answers appear here.
    </p>
  {:else if v.New || v.Form}
    {@const f = v.Form}
    <form  method="post"  action="/forms"  class="card stack"  data-form-editor>
      <h2>{f ? 'Edit form' : 'New form'}</h2>
      {#if f}<input  type="hidden"  name="id"  value={f.ID}>{/if}
      <label>Name <input  name="name"  value={f?.Name ?? ''}  required></label>
      <label>Questions
        <textarea  name="questions"  rows="9">{v.Questions ?? ''}</textarea>
      </label>
      <p  class="muted small">One question per line. Start a line with [text], [choice], [multi], [rating] or [nps] (no tag is a text question), add
        <code>::</code> and choices separated by <code>;</code> after a choice question, and end with <code>*</code> to require it.
        Example: <code>[choice] How did you hear about us? * :: A friend; Search; An event</code></p>
      <div  class="row"><button  type="submit"  class="primary">Save</button><a  class="ghost small"  href="/forms">Cancel</a></div>
    </form>

    {#if f}
      <div  class="card"  data-form-state>
        <h3>{f.Label}</h3>
        <div  class="row">
          {#each [['inProgress', 'Publish now'], ['paused', 'Pause'], ['completed', 'Close'], ['draft', 'Back to draft']] as [st, lab]}
            <form  method="post"  action={"/forms/" + f.ID + "/status"}  class="inline"><input  type="hidden"  name="status"  value={st}><button  class="ghost small">{lab}</button></form>
          {/each}
        </div>
        <form  method="post"  action={"/forms/" + f.ID + "/schedule"}  class="row"  data-schedule>
          <label  class="small">Goes live on <input  type="date"  name="publish_on"  value={f.PublishDate}></label>
          <label  class="small">Closes on <input  type="date"  name="close_on"  value={f.CloseDate}></label>
          <button  class="small">Schedule</button>
        </form>
        <p  class="muted small">A form goes live at the start of the day chosen. Leave both empty and save to clear a schedule.</p>
        <p><a  href={"/forms?id=" + f.ID + "&results=1"}>See the answers</a></p>
        <form  method="post"  action={"/forms/" + f.ID + "/delete"}  data-confirm="Delete this form and every answer to it?"><button  class="ghost small">Delete this form</button></form>
      </div>
    {/if}

    {#if v.Results}
      <h2>Answers ({v.Results.Total}, {v.Results.Finished} finished)</h2>
      {#if v.Results.Responses && v.Results.Responses.length}
        <table  class="list small"  data-results>
          <thead><tr><th>When</th>{#each v.Order as id}<th>{v.Headings[id]}</th>{/each}</tr></thead>
          <tbody>{#each v.Results.Responses as r}<tr><td>{F.date(r.Created)}</td>{#each v.Order as id}<td>{cell(r.Answers?.[id])}</td>{/each}</tr>{/each}</tbody>
        </table>
      {:else}<p  class="muted">No answers yet.</p>{/if}
    {/if}
  {:else}
    <p><a  class="primary small"  href="/forms?new=1">New form</a></p>
    {#if v.Forms && v.Forms.length}
      <table  class="list small"  data-forms>
        <thead><tr><th>Form</th><th>State</th><th>Questions</th><th></th></tr></thead>
        <tbody>
          {#each v.Forms as f}
            <tr  data-form={f.ID}>
              <td><a  href={"/forms?id=" + f.ID}>{f.Name}</a></td>
              <td>{f.Label}{#if f.PublishDate} <span  class="muted">from {f.PublishDate}</span>{/if}</td>
              <td>{(f.Questions ?? []).length}</td>
              <td><a  href={"/forms?id=" + f.ID + "&results=1"}>Answers</a></td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}<p  class="muted">No forms yet.</p>{/if}
  {/if}
</section>
{/snippet}
{@render content(dot)}
