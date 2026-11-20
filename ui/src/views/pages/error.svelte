<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section  class="card centered">
  <h1>{dot?.Status}</h1>
  <p  class="muted">{dot?.Message}</p>
  <a  href={("/")}>Back</a>
</section>
{/snippet}
{@render content(dot)}
