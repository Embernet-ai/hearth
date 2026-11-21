<script lang="ts">
  import type { Page } from '../lib/types';
  import { groups, mine, scan, tail, isOn, visibleItems, type RailItem, type RailGroup } from '../lib/rail';
    import { navOpen, setNavOpen, currentTheme, toggleTheme } from '../lib/store';
  import OrgNode from './OrgNode.svelte';

  let { pg, open = $bindable(false), onSignOut }: { pg: Page; open: boolean; onSignOut: () => void } = $props();

  let theme = $state(currentTheme());
  const contractor = $derived(!!pg.Identity?.Contractor);
  const visible = (items: RailItem[]) => visibleItems(items, pg);
  const name = $derived(pg.Identity?.DisplayName || pg.Identity?.PrincipalID?.slice(0, 8) || '');
</script>

{#snippet item(i: RailItem)}
  {@const n = i.badge?.(pg)}
  <a class="navitem" class:on={isOn(i, pg.Path)} href={i.href}>{i.label}{#if i.href === '/outbox'}<!-- pwa.js keeps this count: what is still waiting on this phone, which the server has never seen -->
    <span class="unread" data-outbox-count={n ?? 0} hidden={!n}>{n ? n : ''}</span>{:else if n}<span class="unread">{n}</span>{/if}</a>
{/snippet}

{#snippet section(g: RailGroup)}
  <details class="orgnode" open={navOpen(g.key, true)} ontoggle={(e) => setNavOpen(g.key, (e.currentTarget as HTMLDetailsElement).open)}>
    <summary><p class="navgroup" data-dot={g.dot}>{g.heading}</p></summary>
    {#each visible(g.items) as i (i.href)}{@render item(i)}{/each}
  </details>
{/snippet}

<aside class="sidenav" class:open>
  <a class="brand" href={'/'}><svg class="mark" viewBox="0 0 64 64" width="24" height="24" aria-hidden="true" focusable="false"><rect width="64" height="64" rx="14" fill="#D92D20"/><rect x="14" y="14" width="9" height="36" rx="3" fill="#fff"/><rect x="41" y="14" width="9" height="36" rx="3" fill="#fff"/><rect x="22" y="33" width="20" height="7" fill="#fff"/><path transform="translate(32 32) scale(.5) translate(-32 -46)" d="M32 46C25 46 20 41 20 34.5 20 28 24 24.5 26.5 20.5 27.7 24 29.5 25.5 31.5 26 30 20 31.5 13 37 8 37.5 14.5 44 20 44 31 44 39.5 38.5 46 32 46Z" fill="#fff"/></svg><span>Hearth</span></a>
  {#if !contractor}<a class="navitem scanlink" class:on={pg.Path === '/scan'} href={scan.href}>Scan</a>{/if}

  <p class="navgroup" data-dot={mine.dot}>{mine.heading}</p>
  {#each visible(mine.items) as i (i.href)}{@render item(i)}{/each}

  {#if pg.Org && pg.Org.length}
    <details class="orgnode" class:can-manage={pg.CanManageSpaces} open={navOpen('g-departments', true)}
      ontoggle={(e) => setNavOpen('g-departments', (e.currentTarget as HTMLDetailsElement).open)}>
      <summary><p class="navgroup" data-dot="plan">Departments</p></summary>
      {#each pg.Org as node (node.ID)}<OrgNode {node} canManage={pg.CanManageSpaces} level={0} />{/each}
    </details>
  {/if}

  {#each groups as g (g.key)}{#if visible(g.items).length}{@render section(g)}{/if}{/each}

  {#each visible(tail) as i (i.href)}{@render item(i)}{/each}

  <div class="drawerfoot">
    <a class="navitem" href={'/account'}>{name}</a>
    <button type="button" class="ghost small" onclick={() => (theme = toggleTheme())} aria-label="Switch between the dark and daylight palettes">
      {theme === 'day' ? 'Dark' : 'Daylight'}
    </button>
    <button type="button" class="small" onclick={onSignOut}>Sign out</button>
  </div>
</aside>
<button type="button" class="navbackdrop" class:open aria-label="Close the menu" onclick={() => (open = false)}></button>
