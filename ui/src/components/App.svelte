<script lang="ts">
  import type { Envelope } from '../lib/types';
  import { getPage, post, PageError } from '../lib/api';
  import { router, intercept } from '../lib/router.svelte';
  import Shell from './Shell.svelte';
  import Login from '../views/Login.svelte';
  import Anonymous from './Anonymous.svelte';
  import { loadAssets } from '../lib/assets';
  import { tick } from 'svelte';
  import { bindInteractions, installInteractions } from '../lib/interact';

  let env = $state<Envelope | null>(null);
  // Changes whenever a different page, or the answer to a form, arrives, so a view is drawn fresh. A refresh in place does not
  // change it: the view stays, and the data under it moves.
  let epoch = $state(0);
  let failure = $state<string | null>(null);
  let loading = $state(true);

  // Load the data for whatever the address bar says, and again whenever it changes.
  $effect(() => {
    const want = router.current;
    const ctl = new AbortController();
    loading = true;
    failure = null;
    getPage(want, ctl.signal)
      .then(({ env: e, url }) => {
        env = e;
        epoch++;
        loadAssets(e.assets);
        // A signed-out request is redirected by the server; show the address it ended at.
        if (url !== want) router.go(url, true);
        askedFor = '';
      })
      .catch((err) => {
        if (ctl.signal.aborted) return;
        // The page could not be had as data. With no signal the service worker answers with the page it last saved (HTML) or its
        // no-signal page; the server's own answer is what knows how to show those, so go there rather than leave a dead end.
        // Once: a second failure for the same address is shown, not retried forever.
        if (err instanceof PageError && /not page data/.test(err.message) && askedFor !== want) { askedFor = want; location.replace(want); return; }
        failure = err instanceof PageError ? err.message : 'Hearth could not be reached. Check the connection and try again.';
      })
      .finally(() => {
        if (!ctl.signal.aborted) loading = false;
      });
    return () => ctl.abort();
  });

  $effect(() => {
    if (env) document.title = env.page.Title ? `${env.page.Title} · Hearth` : 'Hearth';
  });

  // The address the browser was last sent to for a page that would not come as data.
  let askedFor = '';
  try { askedFor = sessionStorage.getItem('hearth.asked') ?? ''; } catch { /* nothing remembered */ }
  $effect(() => {
    try { sessionStorage.setItem('hearth.asked', askedFor); } catch { /* nothing remembered */ }
  });

  function setEnv(e: Envelope, url: string) {
    env = e;
    epoch++;
    loadAssets(e.assets);
    router.go(url, true);
  }

  bindInteractions(setEnv);
  installInteractions();

  // The server says something changed (live.js), or a field saved itself: fetch this page's data again and update in place.
  let refreshing: number | undefined;
  function refresh(ev?: Event) {
    window.clearTimeout(refreshing);
    const now = (ev as CustomEvent | undefined)?.detail?.now;
    refreshing = window.setTimeout(async () => {
      const want = router.current;
      try {
        const { env: e } = await getPage(want);
        if (router.current === want && e.template === env?.template) {
          window.dispatchEvent(new Event('hearth:refresh-start'));
          env = e;
          await tick();
          window.dispatchEvent(new Event('hearth:refresh-end'));
        }
      } catch {
        /* the next change tries again */
      }
    }, now ? 0 : 250);
  }

  $effect(() => {
    window.addEventListener('hearth:refresh', refresh);
    return () => window.removeEventListener('hearth:refresh', refresh);
  });

  async function signOut() {
    const r = await post('/logout', new FormData());
    setEnv(r.env, r.url);
  }
</script>

<svelte:window onclick={intercept} />

{#if failure}
  <main class="centered-page"><section class="card"><h1>Hearth</h1><p class="err">{failure}</p><button type="button" onclick={() => router.go(router.current, true)}>Try again</button></section></main>
{:else if env}
  {#if env.template === 'login'}
    <Login {env} onEnv={setEnv} />
  {:else if !env.page.Identity}
    <!-- A page somebody sees before they are signed in (first-run setup, choosing a password from a link): no rail, no top bar. -->
    <main class="centered-page">{#key epoch}<Anonymous {env} {setEnv} />{/key}</main>
  {:else}
    <Shell {env} {epoch} onEnv={setEnv} onSignOut={signOut} />
  {/if}
{:else if loading}
  <main class="centered-page"><p class="muted">Loading</p></main>
{/if}
