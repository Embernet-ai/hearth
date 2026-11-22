<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
  const list = $derived(dot?.ToolsList?.Tools ?? []);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Tools</h1>
  {#if list.length}
    <div  class="settingsgrid"  data-tools>
      {#each list as t}
        <div  class="card"  data-tool={t.Name}>
          <h3>{t.Name}</h3>
          <p  class="muted">{t.Description}</p>
          <a  class="button"  href={t.Href}  target="_blank"  rel="noopener noreferrer">Open {t.Name}</a>
          {#if t.Note}<p  class="muted small">{t.Note}</p>{/if}
        </div>
      {/each}
    </div>
  {:else}
    <p  class="muted"  data-not-set-up>No tools are set up on this installation. Set <code>HEARTH_TOOLS</code> to a list of cards, each with a name, a description and an address.</p>
  {/if}
</section>
{/snippet}
{@render content(dot)}
