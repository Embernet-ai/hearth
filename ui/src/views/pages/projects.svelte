<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Projects</h1>
  <p  class="muted small">
    {dot?.ProjectCount} project{#if F.t(F.ne(dot?.ProjectCount, 1))}s{/if}, under the
    department each one belongs to. A project is where work is filed, so opening
    one shows everything in it and everything in anything under it.
  </p>

  {#if F.t(dot?.ProjectGroups)}
    {#each F.rng(dot?.ProjectGroups) as [k1, d1]}
      <h3>{d1?.Department}
        <span  class="muted small">{F.len(d1?.Projects)} project{#if F.t(F.ne(F.len(d1?.Projects), 1))}s{/if},
          {d1?.Documents} document{#if F.t(F.ne(d1?.Documents, 1))}s{/if}</span></h3>
      <table  class="list">
        <thead><tr><th>Project</th><th>Holds</th></tr></thead>
        <tbody>
          {#each F.rng(d1?.Projects) as [k2, d2]}
            <tr>
              <td><a  href={("/documents?space=" + S(d2?.ID))}>{d2?.Name}</a></td>
              <td  class="muted">{d2?.Documents} document{#if F.t(F.ne(d2?.Documents, 1))}s{/if}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/each}
  {:else}
    <div  class="empty">
      <strong>No projects</strong>
      A project is a space filed under a department. The space tree is on
      <a  href={("/spaces")}>the spaces page</a>.
    </div>
  {/if}
</section>
{/snippet}
{@render content(dot)}
