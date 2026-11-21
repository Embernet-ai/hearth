<script lang="ts">
  import type { Page } from '../lib/types';
  import { router } from '../lib/router.svelte';
  import { currentTheme, toggleTheme } from '../lib/store';
  import { F } from '../lib/gofuncs';
  import { post } from '../lib/api';

  let { pg, onMenu, onSignOut, onEnv, railHidden = false, onRail = () => {} }: { pg: Page; onMenu: () => void; onSignOut: () => void; onEnv: (e: any, url: string) => void; railHidden?: boolean; onRail?: () => void } = $props();
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
  <button type="button" class="railbtn ghost small" aria-expanded={!railHidden} aria-controls="sidepanel" onclick={onRail}
    title={railHidden ? 'Show the side panel' : 'Hide the side panel'}>
    <svg viewBox="0 0 20 20" width="16" height="16" aria-hidden="true" focusable="false"><rect x="2.5" y="3.5" width="15" height="13" rx="2" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M8 3.5v13" stroke="currentColor" stroke-width="1.6" fill="none"/></svg>
    <span class="railbtn-label">{railHidden ? 'Show menu' : 'Hide menu'}</span>
  </button>
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
