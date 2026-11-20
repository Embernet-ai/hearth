<script lang="ts">
  import type { Page } from '../lib/types';
  import { router } from '../lib/router.svelte';
  import { currentTheme, toggleTheme } from '../lib/store';
  import { F } from '../lib/gofuncs';
  import { post } from '../lib/api';

  let { pg, onMenu, onSignOut, onEnv }: { pg: Page; onMenu: () => void; onSignOut: () => void; onEnv: (e: any, url: string) => void } = $props();
  let theme = $state(currentTheme());
  let q = $state('');
  const name = $derived(pg.Identity?.DisplayName || pg.Identity?.PrincipalID?.slice(0, 8) || '');

  async function stopTimer(e: SubmitEvent) {
    e.preventDefault();
    const r = await post('/timer/stop', new FormData());
    onEnv(r.env, r.url);
  }
  function search(e: SubmitEvent) {
    e.preventDefault();
    router.go(`/search?q=${encodeURIComponent(q)}`);
  }
</script>

<header class="topbar">
  <button type="button" class="menubtn" aria-label="Open the menu" onclick={onMenu}>&#9776;</button>
  <div class="titleblock">
    {#if pg.Crumbs && pg.Crumbs.length}
      <nav class="breadcrumb" aria-label="Location">
        {#each pg.Crumbs as c, i (c.ID)}{#if i}<span class="sep">/</span>{/if}<a href={`/documents?space=${c.ID}`}>{c.Name}</a>{/each}
      </nav>
    {/if}
    <h1 class="pagetitle">{pg.Title}</h1>
  </div>
  <form class="topsearch" onsubmit={search}>
    <input name="q" bind:value={q} placeholder="Search" aria-label="Search" />
  </form>
  {#if pg.Timer}
    <form class="timer" onsubmit={stopTimer}>
      {#if pg.Timer.DocumentID}<a href={`/documents/${pg.Timer.DocumentID}`}>{pg.Timer.DocTitle}</a>{:else}<a href={'/time'}>Working</a>{/if}
      <span class="elapsed">{F.hours(pg.Timer.Elapsed)}</span>
      <button type="submit">Stop</button>
    </form>
  {/if}
  <div class="signout">
    <button type="button" class="ghost small" onclick={() => (theme = toggleTheme())} aria-label="Switch between the dark and daylight palettes">
      {theme === 'day' ? 'Dark' : 'Daylight'}
    </button>
    <a class="who" href={'/account'}>{name}</a>
    <button type="button" onclick={onSignOut}>Sign out</button>
  </div>
</header>
