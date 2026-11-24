<script lang="ts">
  // @ts-nocheck
  let { pg }: { pg: any; onEnv?: any } = $props();
  const d: any = $derived(pg.Desk);
  const kind = $derived(d.DupType === 'company' ? 'companies' : 'contacts');
</script>

<section>
  <div class="row spread">
    <h1>Duplicates</h1>
    <div class="row">
      <a class={'chip' + (d.DupType !== 'company' ? ' on' : '')} href="/crm/duplicates">People</a>
      <a class={'chip' + (d.DupType === 'company' ? ' on' : '')} href="/crm/duplicates?type=company">Companies</a>
    </div>
  </div>
  <p class="muted small">Records that look like one. Pick the one to keep and merge the rest into it: their deals, notes, follow-ups, people and tags move across, blanks are filled in, and nobody who unsubscribed is ever re-subscribed.</p>
  {#each d.Dups || [] as g, i}
    <form method="post" action="/crm/merge" class="card" data-dup-group>
      <input type="hidden" name="type" value={d.DupType === 'company' ? 'company' : 'contact'} />
      <h3>{g.Reason}</h3>
      <table class="list">
        <thead><tr><th>Keep</th><th>Merge in</th><th>Name</th><th>Detail</th><th>Owner</th><th>Deals</th></tr></thead>
        <tbody>
          {#each g.Records as r, j (r.ID)}
            <tr>
              <td><input type="radio" name="survivor" value={r.ID} checked={j === 0} aria-label="Keep this one" /></td>
              <td><input type="checkbox" name="merge" value={r.ID} checked={j !== 0} aria-label="Merge this one in" /></td>
              <td><a href={'/' + kind + '/' + r.ID}>{r.Name}</a></td>
              <td class="muted small">{r.Detail}</td>
              <td class="muted small">{r.Owner}</td>
              <td class="muted small">{r.Deals}</td>
            </tr>
          {/each}
        </tbody>
      </table>
      <div class="row"><button type="submit" data-confirm="Merge the ticked records into the one marked Keep?">Merge</button></div>
    </form>
  {:else}
    <p class="muted">No duplicates found.</p>
  {/each}
</section>
