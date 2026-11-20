<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Tags</h1>
  <p  class="muted small">
    What a logged hour can be sorted by -- "onsite", "remote", whatever billing
    needs. Anybody can apply a tag while logging or correcting time; only an
    admin defines new ones here.
  </p>

  {#if F.t(dot?.Tags)}
    <table  class="list">
      <thead><tr><th  style="width:10rem">Name</th><th>When to use it</th>{#if F.t(dot?.CanManageTags)}<th></th>{/if}</tr></thead>
      <tbody>
        {#each F.rng(dot?.Tags) as [k1, d1]}
          {#if F.t(dot?.CanManageTags)}
            
            <tr>
              <td  colspan="2">
                <form  method="post"  action={"/tags/" + S(d1?.ID)}  class="row">
                  <input  name="name"  value={S(d1?.Name)}  style="width:10rem"  required>
                  <input  name="description"  value={S(d1?.Description)}  placeholder="When to use it"  style="flex:1">
                  <button  type="submit"  class="ghost small">Save</button>
                </form>
              </td>
              <td>
                <form  method="post"  action={"/tags/" + S(d1?.ID) + "/delete"}                        data-confirm={"Delete the " + S(d1?.Name) + " tag? It will be removed from every entry that carries it."}>
                  <button  type="submit"  class="ghost small">Delete</button>
                </form>
              </td>
            </tr>
          {:else}
            <tr><td>{d1?.Name}</td><td  class="muted">{d1?.Description}</td></tr>
          {/if}
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">No tags defined yet.</p>
  {/if}

  {#if F.t(dot?.CanManageTags)}
    <h3>New tag</h3>
    <form  method="post"  action="/tags"  class="row">
      <input  name="name"  placeholder="Name, e.g. Onsite"  style="width:10rem"  required>
      <input  name="description"  placeholder="When to use it"  style="flex:1">
      <button  type="submit">Add</button>
    </form>
  {/if}
</section>
{/snippet}
{@render content(dot)}
