<script lang="ts">
  import type { Page } from '../lib/types';
  let { pg }: { pg: Page } = $props();
</script>

<section class="card">
  <h1>{pg.Title || 'Hearth'}</h1>
  <p>This build of the interface has no view for this page. Reload to get the current one.</p>
</section>
