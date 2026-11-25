<script lang="ts">
  // @ts-nocheck
  import { pager } from './shared.svelte';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const d: any = $derived(pg.Desk);
  const q = (k: string, v: string) => {
    const u = new URLSearchParams({ who: d.Who || '', status: d.Status || 'open', due: d.Due || '', kind: d.Kind || '' });
    u.set(k, v);
    for (const [key, val] of [...u]) if (!val) u.delete(key);
    return '/crm/followups' + (u.toString() ? '?' + u : '');
  };
</script>

<section>
  <div class="row spread">
    <h1>Follow-ups</h1>
    <div class="row">
      <a class="chip" href={'/crm/followups.csv?' + new URLSearchParams({ who: d.Who || '', status: d.Status || 'open', due: d.Due || '' })}>Export CSV</a>
    </div>
  </div>
  <p class="muted small">What the team owes people. Everybody's by default; narrow it to yours with the filter.</p>
  <div class="row wrap" data-followup-filters>
    <a class={'chip' + (!d.Who ? ' on' : '')} href={q('who', '')}>Everyone</a>
    <a class={'chip' + (d.Who === 'me' ? ' on' : '')} href={q('who', 'me')}>Mine</a>
    <a class={'chip' + (d.Who === 'none' ? ' on' : '')} href={q('who', 'none')}>Nobody</a>
    <span class="muted small">|</span>
    <a class={'chip' + (!d.Due ? ' on' : '')} href={q('due', '')}>Any date</a>
    <a class={'chip' + (d.Due === 'overdue' ? ' on' : '')} href={q('due', 'overdue')}>Overdue</a>
    <a class={'chip' + (d.Due === 'today' ? ' on' : '')} href={q('due', 'today')}>Today</a>
    <a class={'chip' + (d.Due === 'week' ? ' on' : '')} href={q('due', 'week')}>Next 7 days</a>
    <span class="muted small">|</span>
    <a class={'chip' + (d.Status === 'open' ? ' on' : '')} href={q('status', 'open')}>Open</a>
    <a class={'chip' + (d.Status === 'done' ? ' on' : '')} href={q('status', 'done')}>Done</a>
    <a class={'chip' + (d.Status === 'all' ? ' on' : '')} href={q('status', 'all')}>All</a>
    <span class="muted small">|</span>
    <a class={'chip' + (!d.Kind ? ' on' : '')} href={q('kind', '')}>Any kind</a>
    {#each ['call', 'email', 'meeting', 'todo'] as k}<a class={'chip' + (d.Kind === k ? ' on' : '')} href={q('kind', k)} data-kind-filter={k}>{k === 'todo' ? 'To do' : k[0].toUpperCase() + k.slice(1)}</a>{/each}
  </div>
  {#if d.Followups?.length}
    <table class="list" data-followup-table>
      <thead><tr><th></th><th>Follow-up</th><th>About</th><th>Due</th><th>Assigned to</th><th></th></tr></thead>
      <tbody>
        {#each d.Followups as f (f.ID)}
          <tr data-followup={f.ID} class={f.Overdue ? 'overdue' : ''}>
            <td><input type="checkbox" name="ids" value={f.ID} form="followupbulk" aria-label="Select" /></td>
            <td><span class="tag" data-kind={f.Kind}>{f.Kind === 'todo' ? 'to do' : f.Kind}</span> {f.Title}{#if f.Body}<div class="muted small">{f.Body}</div>{/if}</td>
            <td><a href={f.TargetHref}>{f.TargetName}</a> <span class="muted small">{f.Target.Type}</span></td>
            <td>
              <form method="post" action={'/crm/followups/' + f.ID + '/due'} class="row">
                <input type="hidden" name="back" value={pg.Path} />
                <input name="due" type="date" value={f.DueAt ? f.DueAt.slice(0, 10) : ''} aria-label="Due" data-autosubmit />
              </form>
            </td>
            <td class="muted small">{f.AssigneeName}</td>
            <td class="row">
              {#each [1, 3, 7] as n}
                <form method="post" action={'/crm/followups/' + f.ID + '/snooze'} class="inline" data-snooze={n}>
                  <input type="hidden" name="days" value={n} />
                  <input type="hidden" name="back" value={pg.Path} />
                  <button type="submit" class="ghost small" title={'Push it out ' + n + ' day' + (n > 1 ? 's' : '')}>+{n}d</button>
                </form>
              {/each}
              <form method="post" action={'/crm/followups/' + f.ID + '/done'} class="inline">
                <input type="hidden" name="done" value={f.Status === 'done' ? '0' : '1'} />
                <input type="hidden" name="back" value={pg.Path} />
                <button type="submit" class="ghost small">{f.Status === 'done' ? 'Open again' : 'Done'}</button>
              </form>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
    <form id="followupbulk" method="post" action="/crm/followups/bulk" class="row wrap" data-followup-bulk>
      <input type="hidden" name="back" value={pg.Path} />
      <span class="muted small">With the ticked rows:</span>
      <button type="submit" name="done" value="1" class="ghost small">Mark done</button>
      <button type="submit" name="done" value="0" class="ghost small">Open again</button>
    </form>
    {@render pager(pg.Pager)}
  {:else}
    <p class="muted">Nothing is owed. Add a follow-up from any company, person, or deal.</p>
  {/if}
</section>
