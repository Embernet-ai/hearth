<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section  class="card">
  <h1>Set-password link</h1>
  <p>For <strong>{dot?.LinkName}</strong> ({dot?.LinkEmail}). It works once and expires {dot?.LinkExpires}.</p>
  {#if F.t(dot?.LinkEmailed)}
    <p  class="muted">Hearth has emailed it to {dot?.LinkEmail}.</p>
  {:else}
    <p  class="muted">Hearth has nowhere to send email from, so copy the link and pass it to them yourself.</p>
  {/if}
  <p><input  type="text"  readonly value={S(dot?.LinkURL)}  aria-label="Set-password link"  style="width:100%"></p>
  <p  class="muted small">This is the only time the link is shown. Hearth keeps just a fingerprint of it, so it cannot be read back later. If it is lost, make another.</p>
  <p><a  class="button"  href={("/people")}>Back to people</a></p>
</section>
{/snippet}
{@render content(dot)}
