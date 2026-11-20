<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<h1>{dot?.Heading}</h1>
<div  class="scan"  data-scan>
  <p  class="muted">
    Point the camera at the label on the machine, or type the code printed
    under the symbol.
  </p>

  
  <button  type="button"  class="primary big"  data-scan-start>Use the camera</button>
  <video  data-scan-video playsinline muted hidden></video>
  <p  class="muted small"  data-scan-status></p>

  
  <form  class="row"  method="get"  action="/scan"  data-scan-manual>
    <label  for="code"  class="muted">Code</label>
    <input  id="code"  name="code"  placeholder="CV-003"  autocapitalize="characters"             autocomplete="off"  spellcheck="false"  inputmode="text"  required>
    <button  type="submit"  class="big">Go</button>
  </form>
</div>

<p  class="muted small">
  Every asset can carry a printed label with its code and a symbol. Open a
  machine and choose Label to print one.
</p>
{/snippet}
{@render content(dot)}
