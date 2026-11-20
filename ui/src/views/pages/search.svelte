<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Search</h1>

  <form  class="row searchbar"  method="get"  action="/search">
    <input  name="q"  value={S(dot?.SearchText)}  autofocus placeholder="a symptom, an asset tag, a sentence from the middle of a runbook">
    <select  name="kind">
      <option  value="">Everything</option>
      <option  value="work_order"  {...(F.t(F.eq(dot?.Kind, "work_order")) ? {"selected": true} : {})}>Work orders</option>
      <option  value="asset"  {...(F.t(F.eq(dot?.Kind, "asset")) ? {"selected": true} : {})}>Assets</option>
      <option  value="task"  {...(F.t(F.eq(dot?.Kind, "task")) ? {"selected": true} : {})}>Tasks</option>
      <option  value="doc"  {...(F.t(F.eq(dot?.Kind, "doc")) ? {"selected": true} : {})}>Documents</option>
    </select>
    <button  type="submit"  class="primary">Search</button>
  </form>

  {#if F.t(dot?.Results)}{@const w1 = dot?.Results}
    <p  class="muted small">
      {#if F.t(dot?.Hits)}{F.len(dot?.Hits)} result{#if F.t(F.ne(F.len(dot?.Hits), 1))}s{/if}{:else}Nothing found{/if}
      · answered by <code>{w1?.Engine}</code> in {F.duration(w1?.Took)}
      {#if F.t(F.eq(w1?.Engine, "postgres"))}
        
        <span  class="warn">· the search cluster is not answering, so these
        results come from the database and will be less forgiving of typos</span>
      {/if}
    </p>
  {/if}

  {#if F.t(dot?.Hits)}
    <ul  class="hits">
      {#each F.rng(dot?.Hits) as [k2, d2]}
        
        <li  data-hit={S(d2?.EntityType) + ":" + S(d2?.EntityID)}>
          <a  href={(S(d2?.Href))}>{#if F.t(d2?.Title)}{d2?.Title}{:else}Untitled{/if}</a>
          <span  class="tag">{#if F.t(d2?.Kind)}{d2?.Kind}{:else}{d2?.EntityType}{/if}</span>
          {#if F.t(d2?.HTML)}<p  class="snippet">{d2?.HTML}</p>{/if}
          <span  class="muted small">{F.ago(d2?.UpdatedAt)}</span>
        </li>
      {/each}
    </ul>
  {:else if F.t(dot?.SearchText)}
    <p  class="muted">
      Nothing matched. Attachments are searchable by their contents once they
      have been read, which happens in the background shortly after upload.
    </p>
  {/if}
</section>
{/snippet}
{@render content(dot)}
