<script lang="ts">
  // @ts-nocheck
  import { F } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
  const v = $derived(dot?.AutomationArea);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Automation</h1>
  {#if F.t(v?.Notice)}<p  class="card small"  data-flash>{v?.Notice}</p>{/if}

  {#if !v?.Configured}
    <p  class="muted"  data-not-set-up>Automation is not connected on this installation. Set <code>HEARTH_CASCADE_URL</code> and <code>HEARTH_CASCADE_API_KEY</code> to the address and key of your workflow engine.</p>
  {:else}
    {#if v.HasNumbers}
      <div  class="numgrid"  data-automation-numbers>
        <div  class="card"><div  class="muted small">Runs, last 24 hours</div><strong>{v.Day.Total}</strong><div  class="muted small">{v.Day.Failed} failed</div></div>
        <div  class="card"><div  class="muted small">Runs, last 7 days</div><strong>{v.Week.Total}</strong><div  class="muted small">{v.Week.Failed} failed</div></div>
        <div  class="card"><div  class="muted small">Running now</div><strong>{v.Day.Unfinished}</strong></div>
        <div  class="card"><div  class="muted small">Waiting on a person</div><strong>{(v.Approvals ?? []).length}</strong></div>
      </div>
    {/if}

    {#if v.Approvals && v.Approvals.length}
      <h2>Waiting on a person</h2>
      <ul  class="list"  data-approvals>{#each v.Approvals as a}<li><strong>{a.Title}</strong> <span  class="muted small">{a.WorkflowName}, {(a.RequestedAt ?? '').slice(0, 16).replace('T', ' ')}</span> <a  href={v.EditorURL + '/approvals'}  target="_blank"  rel="noopener">Answer in Cascade</a></li>{/each}</ul>
    {/if}

    <h2>Workflows</h2>
    {#if v.Workflows && v.Workflows.length}
      <div  class="tablescroll"><table  class="table"  data-workflows>
        <thead><tr><th>Workflow</th><th>State</th><th>Last run</th><th></th></tr></thead>
        <tbody>
        {#each v.Workflows as w}
          <tr  data-workflow={w.ID}>
            <td><a  href={v.EditorURL + '/workflows/' + w.ID}  target="_blank"  rel="noopener">{w.Name}</a></td>
            <td>{w.Active ? 'On' : 'Off'}</td>
            <td>{#if w.LastStatus}{w.LastStatus} <span  class="muted small">{w.LastAt}</span>{:else}<span  class="muted small">none lately</span>{/if}</td>
            <td  class="row">
              <form  method="post"  action={'/automation/workflows/' + w.ID + '/run'}  class="row">
                <input  name="input"  placeholder={'{"deal": "..."} (optional)'}  aria-label="Input as JSON">
                <button  type="submit">Run now</button>
              </form>
              <form  method="post"  action={'/automation/workflows/' + w.ID + '/active'}>
                <input  type="hidden"  name="active"  value={w.Active ? '0' : '1'}>
                <button  type="submit">{w.Active ? 'Switch off' : 'Switch on'}</button>
              </form>
            </td>
          </tr>
        {/each}
        </tbody>
      </table></div>
    {:else}<p  class="muted">No workflows yet. Build one in Cascade.</p>{/if}

    <h2>Runs</h2>
    <nav  class="row tabs">
      <a  class={'chip' + (!v.Status ? ' on' : '')}  href="/automation">All</a>
      <a  class={'chip' + (v.Status === 'failed' ? ' on' : '')}  href="/automation?status=failed">Failed</a>
      <a  class={'chip' + (v.Status === 'running' ? ' on' : '')}  href="/automation?status=running">Running</a>
    </nav>
    {#if v.Record}<p  class="muted small"  data-record>Runs that mention <code>{v.Record}</code>. <a  href="/automation">Clear</a></p>{/if}
    {#if v.Executions && v.Executions.length}
      <div  class="tablescroll"><table  class="table"  data-runs>
        <thead><tr><th>Workflow</th><th>Status</th><th>When</th><th>Took</th><th></th></tr></thead>
        <tbody>
        {#each v.Executions as e}
          <tr  data-run={e.ID}>
            <td>{e.WorkflowName}</td>
            <td>{e.Status}{#if e.Error} <span  class="muted small">{e.Error}</span>{/if}</td>
            <td>{e.When}</td>
            <td>{e.Duration}</td>
            <td  class="row">
              {#if e.CanStop}
                <form  method="post"  action={'/automation/executions/' + e.ID + '/stop'}><button  type="submit">Stop</button></form>
              {:else}
                <form  method="post"  action={'/automation/executions/' + e.ID + '/retry'}><button  type="submit">Retry</button></form>
              {/if}
            </td>
          </tr>
        {/each}
        </tbody>
      </table></div>
    {:else}<p  class="muted"  data-no-runs>No runs to show.</p>{/if}
  {/if}
</section>
{/snippet}
{@render content(dot)}

<style>
  .tablescroll { overflow-x: auto; max-width: 100%; }
  .numgrid { display: grid; gap: var(--s-3); grid-template-columns: repeat(auto-fill, minmax(min(100%, 11rem), 1fr)); margin: var(--s-2) 0 var(--s-5); }
</style>
