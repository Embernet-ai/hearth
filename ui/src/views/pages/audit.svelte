<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<h1>{dot?.Heading}</h1>
<p  class="muted">
  Every permission change, every deletion, and every export, with who did it.
  The table this comes from refuses an update outright and refuses a delete
  unless a whole organisation is being removed, so what is on this page is what
  happened.
  <span  class="muted small">{dot?.AuditCount} entries</span>
</p>

<form  class="viewbar"  method="get"  action="/audit">
  <div>
    <label  for="action">Action</label>
    <select  id="action"  name="action">
      <option  value="">everything</option>
      {#each F.rng(dot?.AuditKinds) as [k1, d1]}
        <option  value={S(d1)} {...(F.t(F.eq(d1, dot?.AuditFilter?.Action)) ? {"selected": true} : {})}>{d1}</option>
      {/each}
    </select>
  </div>
  <div>
    <label  for="since">Since</label>
    <input  id="since"  type="date"  name="since"  value={S(F.datefield(dot?.AuditFilter?.Since))}>
  </div>
  <div>
    <label  for="entity">About</label>
    <input  id="entity"  name="entity"  value={S(dot?.AuditFilter?.EntityID)}             placeholder="an entity id"  style="width:22rem">
  </div>
  <button  type="submit">Show</button>
  <a  class="chip"  href={("/audit")}>Clear</a>
</form>

{#if F.t(dot?.Audit)}
  <table  class="list"  data-audit>
    <thead>
      <tr><th>When</th><th>Who</th><th>Action</th><th>What</th><th>Detail</th></tr>
    </thead>
    <tbody>
      {#each F.rng(dot?.Audit) as [k2, d2]}
        <tr  data-entry={S(d2?.ID)}  data-action={S(d2?.Action)}>
          <td><span  title={S(d2?.At)}>{F.ago(d2?.At)}</span></td>
          <td>
            
            {#if F.t(d2?.ActorID)}
              <span  data-actor={S(F.deref_s(d2?.ActorID))}>{#if F.t(d2?.ActorName)}{d2?.ActorName}{:else}{F.short(F.deref_s(d2?.ActorID))}{/if}</span>
            {:else}
              <span  class="muted">nobody, automatic</span>
            {/if}
            {#if F.t(d2?.IP)}<span  class="muted small">{d2?.IP}</span>{/if}
          </td>
          <td><code>{d2?.Action}</code></td>
          <td>
            {#if F.t(d2?.EntityID)}
              {#if F.t(F.eq(d2?.EntityType, "document"))}
                <a  href={("/documents/" + S(F.deref_s(d2?.EntityID)))}>{F.short(F.deref_s(d2?.EntityID))}</a>
              {:else}
                <span  class="tag">{d2?.EntityType}</span>
                <code  class="muted small">{F.short(F.deref_s(d2?.EntityID))}</code>
              {/if}
            {/if}
          </td>
          <td  class="error-cell">
            {#if F.t(d2?.Before)}<code  class="muted small">was {d2?.Before}</code>{/if}
            {#if F.t(d2?.After)}<code  class="muted small">now {d2?.After}</code>{/if}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
  {#if F.t(dot?.AuditMore)}
    <p><a  class="chip"  href={("/audit?before=" + S(dot?.AuditMore) + S((F.t(dot?.AuditFilter?.Action) ? ("&action=" + S(dot?.AuditFilter?.Action)) : "")))}>Older</a></p>
  {/if}
{:else}
  <p  class="muted small">Nothing matches that.</p>
{/if}
{/snippet}
{@render content(dot)}
