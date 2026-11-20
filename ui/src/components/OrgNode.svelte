<script lang="ts">
  import type { OrgNode } from '../lib/types';
    import { navOpen, setNavOpen } from '../lib/store';
  import Self from './OrgNode.svelte';

  let { node, canManage = false, level = 0 }: { node: OrgNode; canManage?: boolean; level?: number } = $props();
  const key = $derived(`s-${node.ID}`);
  // svelte-ignore state_referenced_locally
  let open = $state(navOpen(`s-${node.ID}`, !!node.Open));
</script>

{#snippet row(n: OrgNode)}
  <span class="chev" aria-hidden="true"></span>
  <a class="navitem dept" class:on={n.Selected} href={`/documents?space=${n.ID}`} title={n.Name} aria-label={n.Documents ? `${n.Name}, ${n.Documents}` : n.Name}>
    <span class="dname">{n.Name}</span>{#if n.Documents}<span class="ncount">{n.Documents}</span>{/if}
  </a>
  <a class="addfolder" href={`/spaces?parent=${n.ID}`} title={`Add a folder in ${n.Name}`} aria-label="Add a folder">+</a>
{/snippet}

{#if node.Children && node.Children.length}
  <details class="orgnode lvl{Math.min(level, 3)}" style={`--lvl:${level}`} {open} ontoggle={(e) => { open = (e.currentTarget as HTMLDetailsElement).open; setNavOpen(key, open); }}>
    <summary class="orgrow">
      {@render row(node)}
    </summary>
    {#each node.Children as child (child.ID)}
      <Self node={child} {canManage} level={level + 1} />
    {/each}
  </details>
{:else}
  <span class="navrow orgrow lvl{Math.min(level, 3)}" style={`--lvl:${level}`}>
    {@render row(node)}
  </span>
{/if}
