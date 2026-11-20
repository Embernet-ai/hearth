<script lang="ts">
  import { onMount } from 'svelte';
  import type { Envelope } from '../lib/types';
  import { getPage, PageError } from '../lib/api';
  import { view } from '../views';
  import { announce, loadAssets } from '../lib/assets';

  // A page drawn inside another: the quick view of a task, the new panel. Its data is the same page data the address
  // would return on its own, and what it shows is the same view, so what is allowed and what is drawn can not drift.
  // A form inside it posts and the view reloads itself; the page behind it stays where it is.
  let { url }: { url: string } = $props();

  let env = $state<Envelope | null>(null);
  let failure = $state('');
  let epoch = $state(0);
  let host = $state<HTMLElement | null>(null);
  const View = $derived(env ? view(env.template) : null);

  async function load(fresh: boolean) {
    try {
      const r = await getPage(url);
      await loadAssets(r.env.assets);
      env = r.env;
      failure = '';
      if (fresh) epoch++;
    } catch (e) {
      failure = e instanceof PageError ? e.message : 'Hearth could not be reached. Check the connection and try again.';
    }
  }

  $effect(() => {
    url;
    env = null;
    load(true);
  });

  onMount(() => {
    const live = () => load(false);
    const again = () => load(true);
    window.addEventListener('hearth:refresh', live);
    host?.addEventListener('hearth:reload', again);
    return () => {
      window.removeEventListener('hearth:refresh', live);
      host?.removeEventListener('hearth:reload', again);
    };
  });

  $effect(() => {
    epoch;
    env;
    if (host && env) queueMicrotask(() => announce(host));
  });
</script>

<div data-embedded bind:this={host}>
  {#if env && View}
    {#key epoch}<View pg={env.page} onEnv={() => {}} />{/key}
  {:else if failure}
    <p class="err">{failure}</p>
  {:else}
    <p class="muted">Loading</p>
  {/if}
</div>
