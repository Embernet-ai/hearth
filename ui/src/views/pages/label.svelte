<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section  class="label-page">
  <div  class="row spread noprint">
    <h1>Label</h1>
    <div  class="row">
      <a  class="chip"  href={("/documents/" + S(dot?.Doc?.ID))}>Back to the asset</a>
      <button  type="button"  class="ghost small"  data-print>Print</button>
    </div>
  </div>

  
  <div  class="label"  data-label={S(dot?.AssetCode)}>
    <div  class="label-code">{dot?.AssetCode}</div>
    <div  class="label-symbol">{@html dot?.QR}</div>
    <div  class="label-title">{dot?.Doc?.Title}</div>
    
    <div  class="label-url">{dot?.ScanURL}</div>
  </div>

  <p  class="muted small noprint">
    Scanning this opens the asset. Somebody who has signed out lands on the sign
    in page and comes back here afterwards, rather than on a document list with
    no idea which machine they were standing at.
  </p>
</section>
{/snippet}
{@render content(dot)}
