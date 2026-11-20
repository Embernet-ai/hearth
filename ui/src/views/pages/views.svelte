<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Views</h1>
  <p  class="muted small">
    A view stores the question, never the answers. Two people opening the same
    one see the rows they are each allowed to see.
  </p>

  {#if F.t(dot?.SavedViews)}
    <table  class="list">
      <thead>
        <tr><th>Name</th><th>Shows</th><th>As</th><th>Shared</th><th></th></tr>
      </thead>
      <tbody>
        {#each F.rng(dot?.SavedViews) as [k1, d1]}
          <tr>
            <td><a  href={(S(d1?.Path))}>{d1?.Name}</a></td>
            <td  class="muted">{#if F.t(d1?.Query?.Kind)}{d1?.Query?.Kind}{:else}everything{/if}
              {#if F.t(d1?.Query?.Filters)}
                <span  class="muted small">· {F.len(d1?.Query?.Filters)} filter{#if F.t(F.ne(F.len(d1?.Query?.Filters), 1))}s{/if}</span>
              {/if}
            </td>
            <td><span  class="tag">{d1?.Kind}</span></td>
            <td  class="muted">{#if F.t(d1?.Shared)}shared{:else}private{/if}</td>
            <td>
              
              {#if F.t(F.eq(d1?.OwnerID, dot?.Me))}
                <form  method="post"  action={"/views/" + S(d1?.ID) + "/delete"}  class="inline"                        data-confirm={"Delete the view " + S(d1?.Name) + "?"}>
                  <button  type="submit"  class="ghost small">Delete</button>
                </form>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">
      No saved views yet. Filter a list the way you want it and give it a name.
    </p>
  {/if}
</section>
{/snippet}
{@render content(dot)}
