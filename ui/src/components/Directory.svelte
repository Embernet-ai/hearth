<script lang="ts">
  // The department as a directory: the folders inside the level you are standing on, drawn from the same tree the side
  // panel draws (pg.Org), so the two never disagree on a name, an order, a nesting or a count. Opening one goes into it.
  import type { OrgNode } from '../lib/types';
  let { pg }: { pg: any } = $props();

  function find(nodes: OrgNode[] | null | undefined, id: string): OrgNode | null {
    for (const n of nodes ?? []) {
      if (n.ID === id) return n;
      const hit = find(n.Children, id);
      if (hit) return hit;
    }
    return null;
  }
  const id = $derived(pg.SpaceID ?? '');
  const node = $derived(id ? find(pg.Org, id) : null);
  // At the top of the tree the directory is every department; inside one it is that department's folders.
  const entries = $derived<OrgNode[]>(id ? (node?.Children ?? []) : (pg.Org ?? []));
  // Projects are lists of work the side panel keeps out of its tree; they stay reachable here, under the folders.
  const projects = $derived((pg.SpaceChildren ?? []).filter((s: any) => s.Kind === 'project'));
  const known = $derived(!id || !!node);
  const insideHref = $derived(`/documents?space=${id}${pg.Query?.Direct ? '&inside=1' : ''}`);
  const crumbs = $derived(pg.Crumbs ?? []);
</script>

{#if known || projects.length}
<section class="directory" data-directory aria-label="Folders">
  <nav class="dirpath" data-dir-path aria-label="Where you are">
    <a href="/documents">Departments</a>
    {#each crumbs as c, i (c.ID)}
      <span class="sep" aria-hidden="true">/</span>
      {#if i === crumbs.length - 1}<strong aria-current="page">{c.Name}</strong>{:else}<a href={`/documents?space=${c.ID}`}>{c.Name}</a>{/if}
    {/each}
  </nav>

  {#if entries.length}
    <ul class="dirgrid" data-dir-entries>
      {#each entries as n (n.ID)}
        <li class="direntry" data-space={n.ID}>
          <a class="dirlink" href={`/documents?space=${n.ID}`} title={n.Name}>
            <span class="dirglyph" aria-hidden="true"></span>
            <span class="dirname">{n.Name}</span>
            {#if n.Documents}<span class="dircount">{n.Documents}</span>{/if}
          </a>
          {#if n.Children && n.Children.length}
            <ul class="dirsub">
              {#each n.Children as c (c.ID)}
                <li data-space={c.ID}><a href={`/documents?space=${c.ID}`} title={c.Name}><span class="dirname">{c.Name}</span>{#if c.Documents}<span class="dircount">{c.Documents}</span>{/if}</a></li>
              {/each}
            </ul>
          {/if}
        </li>
      {/each}
    </ul>
  {:else if id && node}
    <p class="muted small" data-dir-empty>No folders in {node.Name}.</p>
  {/if}

  {#if projects.length}
    <h3 class="dirhead">Projects</h3>
    <ul class="dirgrid" data-dir-projects>
      {#each projects as p (p.ID)}
        <li class="direntry" data-space={p.ID}>
          <a class="dirlink" href={`/documents?space=${p.ID}`} title={p.Name}>
            <span class="dirglyph list" aria-hidden="true"></span>
            <span class="dirname">{p.Name}</span>
            {#if p.Documents}<span class="dircount">{p.Documents}</span>{/if}
          </a>
        </li>
      {/each}
    </ul>
  {/if}

  {#if id && (entries.length || projects.length)}
    <p class="dirscope small">
      {#if pg.Query?.Direct}Showing what is directly in this level. <a href={insideHref} data-dir-inside>Show everything inside</a>
      {:else}Showing everything inside this level. <a href={`/documents?space=${id}`} data-dir-here>Show only this level</a>{/if}
    </p>
  {/if}
</section>
{/if}

<style>
  .directory { margin: 0 0 var(--s-4); }
  .dirpath { font-size: var(--t-sm); margin: 0 0 var(--s-3); display: flex; flex-wrap: wrap; gap: var(--s-1) var(--s-2); align-items: baseline; }
  .dirpath .sep { color: var(--muted); }
  .dirgrid { list-style: none; margin: 0; padding: 0; display: grid; gap: var(--s-3); grid-template-columns: repeat(auto-fill, minmax(min(100%, 17rem), 1fr)); align-items: start; }
  .direntry { border: 1px solid var(--line); border-radius: 8px; padding: var(--s-2) var(--s-3); min-width: 0; }
  .dirlink { display: flex; align-items: center; gap: var(--s-2); text-decoration: none; color: var(--fg); font-weight: 600; min-width: 0; }
  .dirname { overflow-wrap: anywhere; min-width: 0; }
  .dircount { margin-left: auto; color: var(--muted); font-size: var(--t-xs); font-weight: 400; font-variant-numeric: tabular-nums; flex: none; }
  .dirglyph { flex: none; width: 1.1rem; height: 0.85rem; border: 1.5px solid currentColor; border-radius: 2px 2px 3px 3px; position: relative; opacity: 0.7; }
  .dirglyph::before { content: ''; position: absolute; left: -1.5px; top: -4px; width: 0.55rem; height: 3px; border: 1.5px solid currentColor; border-bottom: 0; border-radius: 2px 2px 0 0; }
  .dirglyph.list { border-radius: 2px; }
  .dirglyph.list::before { display: none; }
  .dirsub { list-style: none; margin: var(--s-2) 0 0; padding: 0 0 0 var(--s-3); border-left: 1px solid var(--line); display: grid; gap: var(--s-1); }
  .dirsub a { display: flex; gap: var(--s-2); text-decoration: none; color: var(--fg); font-size: var(--t-sm); }
  .dirhead { margin: var(--s-4) 0 var(--s-2); font-size: var(--t-sm); color: var(--muted); font-weight: 600; }
  .dirscope { margin: var(--s-3) 0 0; color: var(--muted); }
</style>
