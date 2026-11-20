<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section  class="card stack">
  <div  class="row spread">
    <h1>Favorites</h1>
    <span  class="row">
      <a  class="chip"  href={("/companies")}>Companies</a>
      <a  class="chip"  href={("/contacts")}>Contacts</a>
      <a  class="chip"  href={("/deals")}>Deals</a>
    </span>
  </div>
  {#if F.t(dot?.FavoriteItems)}
    <table  class="list"  data-favorites>
      <thead><tr><th>What</th><th>Kind</th><th>Detail</th></tr></thead>
      <tbody>
        {#each F.rng(dot?.FavoriteItems) as [k1, d1]}
          <tr>
            <td><a  href={(S(d1?.Href))}>{d1?.Name}</a></td>
            <td><span  class="tag">{d1?.Kind}</span></td>
            <td  class="muted small">{d1?.Detail}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">Nothing starred yet. Open a company, a contact or a deal and use its Favorite button to keep it here.</p>
  {/if}
</section>
{/snippet}
{@render content(dot)}
