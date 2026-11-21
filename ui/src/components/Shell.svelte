<script lang="ts">
  import type { Envelope } from '../lib/types';
    import { post } from '../lib/api';
  import Rail from './Rail.svelte';
  import Topbar from './Topbar.svelte';
  import { view } from '../views';
  import { loadAssets, announce } from '../lib/assets';
  import { navOpen, setNavOpen } from '../lib/store';
  import { fills } from '../lib/fill';
  import { tick } from 'svelte';

  let { env, epoch, onEnv, onSignOut }: { env: Envelope; epoch: number; onEnv: (e: Envelope, url: string) => void; onSignOut: () => void } = $props();
  let menuOpen = $state(false);
  // The side panel folds away on a wide screen so the page has the whole width, and comes back with the same button. Remembered per browser.
  let railHidden = $state(navOpen('rail-hidden', false));
  const toggleRail = () => { railHidden = !railHidden; setNavOpen('rail-hidden', railHidden); };
  const pg = $derived(env.page);
  const View = $derived(view(env.template));
  const fill = $derived(fills(env.template));

  let mainEl = $state<HTMLElement | null>(null);
  // After each page is drawn, tell the behavior scripts there is new markup to set up.
  $effect(() => {
    const assets = env.assets;
    epoch;
    // After the page is drawn and the behavior scripts have loaded: on a hard load the scripts are still arriving when the page draws,
    // and the signal they listen for would otherwise be missed.
    Promise.all([tick(), loadAssets(assets)]).then(() => announce(mainEl));
  });

  // What live.js needs to know about this page: the build that drew it (a different one reported later means reload) and the
  // document it shows (its comments arrive live).
  $effect(() => {
    document.body.setAttribute('data-build', pg.Build.Revision || '');
    const doc = env.template === 'document' ? (pg.Doc as { ID?: string } | undefined)?.ID : '';
    if (doc) document.body.setAttribute('data-doc', doc);
    else document.body.removeAttribute('data-doc');
  });

  async function leavePlayground(e: SubmitEvent) {
    e.preventDefault();
    const r = await post('/tutorial/leave', new FormData());
    onEnv(r.env, r.url);
  }
</script>

<div class="app" class:railhidden={railHidden}>
  <Rail {pg} bind:open={menuOpen} {onSignOut} />
  <div class="workspace">
    <Topbar {pg} onMenu={() => (menuOpen = true)} {onSignOut} {onEnv} {railHidden} onRail={toggleRail} />
    {#if pg.Identity?.ActingFor}
      <div class="playbanner" role="status">
        <span><strong>PLAYGROUND</strong> &middot; your practice company. Nothing here is real, and nobody else sees it.</span>
        <a href={'/settings/tutorial'}>Lessons &amp; reset</a>
        <form class="inline" onsubmit={leavePlayground}><button type="submit" class="small">Back to Hearth</button></form>
      </div>
    {/if}
    <main class:wide={pg.Wide || fill} class:fill data-screen={env.template} bind:this={mainEl}>
      {#if pg.NeedsDepartment}
        <p class="notice">Which department are you in? Tell us, and a manager or an admin will confirm it.
          <a class="chip" href={'/departments/request'}>Choose my department</a></p>
      {/if}
      {#key epoch}<View {pg} {onEnv} />{/key}
    </main>
    <footer>
      <span title={pg.Build.Revision}>{pg.Version || pg.Build.Revision.slice(0, 8)}</span>
      <span>{pg.Build.Profile}</span>
    </footer>
  </div>
</div>
