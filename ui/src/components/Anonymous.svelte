<script lang="ts">
  import type { Envelope } from '../lib/types';
  import { view } from '../views';

  // The view for a page that is shown before anybody is signed in. Kept apart so the app and the acceptance suite's renderer
  // draw it the same way.
  let { env, setEnv }: { env: Envelope; setEnv: (e: Envelope, url: string) => void } = $props();
  const View = $derived(view(env.template));
</script>

<View pg={env.page} onEnv={setEnv} />
