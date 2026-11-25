<script lang="ts">
  // @ts-nocheck
  let { pg }: { pg: any; onEnv?: any } = $props();
  const d: any = $derived(pg.Desk);
  const kinds = [['contact', 'People'], ['company', 'Companies'], ['deal', 'Deals']];
  const label = (t: string) => ({ text: 'Text', number: 'Number', currency: 'Currency', date: 'Date', boolean: 'Yes or no', select: 'Choice', url: 'Web address', email: 'Email', phone: 'Phone' })[t] ?? t;
</script>

<section>
  <h1>Custom fields</h1>
  <p class="muted small">Fields your team adds for what it tracks. They show on every record of that kind, anybody who can edit the record fills them in, and they stay in Hearth.</p>
  <div class="row">
    {#each kinds as [k, name]}<a class={'chip' + (d.PropKind === k ? ' on' : '')} href={'/crm/properties?type=' + k}>{name}</a>{/each}
  </div>
  {#if d.PropDefs?.length}
    <table class="list" data-property-defs>
      <thead><tr><th>Field</th><th>Key</th><th>Kind of value</th><th>Choices</th>{#if d.CanManage}<th></th>{/if}</tr></thead>
      <tbody>
        {#each d.PropDefs as p (p.ID)}
          <tr data-property={p.Key}>
            <td>{p.Label}</td><td class="muted small">{p.Key}</td><td>{label(p.DataType)}</td><td class="muted small">{(p.Options || []).join(', ')}</td>
            {#if d.CanManage}
              <td class="row">
                <details class="inline">
                  <summary class="ghost small">Edit</summary>
                  <form method="post" action="/crm/properties" class="fields">
                    <input type="hidden" name="id" value={p.ID} />
                    <input type="hidden" name="type" value={d.PropKind} />
                    <label>Label<input name="label" value={p.Label} required /></label>
                    {#if p.DataType === 'select'}<label>Choices, one per line<textarea name="options" rows="4">{(p.Options || []).join('\n')}</textarea></label>{/if}
                    <div class="row"><button type="submit">Save</button></div>
                  </form>
                </details>
                <form method="post" action={'/crm/properties/' + p.ID + '/delete'} class="inline">
                  <input type="hidden" name="type" value={d.PropKind} />
                  <button type="submit" class="ghost small" data-confirm="Remove this field and everything people entered in it?">Remove</button>
                </form>
              </td>
            {/if}
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p class="muted">No custom fields for this kind yet.</p>
  {/if}
  {#if d.CanManage}
    <h2>Add a field</h2>
    <form method="post" action="/crm/properties" class="fields" data-new-property>
      <input type="hidden" name="type" value={d.PropKind} />
      <label>Label<input name="label" required /></label>
      <label>Kind of value
        <select name="data_type">{#each d.PropTypes || [] as t}<option value={t}>{label(t)}</option>{/each}</select></label>
      <label>Choices, one per line (for a choice field)<textarea name="options" rows="3"></textarea></label>
      <div class="row"><button type="submit">Add field</button></div>
    </form>
  {:else}
    <p class="muted small">Only an admin adds or removes fields.</p>
  {/if}
</section>
