<script lang="ts">
  // The team's parts of the dashboard: one section per tool the installation runs (email marketing, social, forms), each a few
  // numbers read from the tool itself and the lines that sit behind them. The server sends it only to the people who work
  // marketing and to super admins (internal/web/teampanel.go), so there is nothing to hide here.
  interface Tile { Label: string; Value: string; Detail?: string; Period: string; Href: string; Tone?: string; Exact?: string }
  interface Row { Title: string; Detail?: string; Href: string }
  interface Section { Key: string; Title: string; Href: string; Tiles?: Tile[] | null; Rows?: Row[] | null; Notice?: string }
  let { team }: { team: { Sections?: Section[] | null } | null | undefined } = $props();
  const sections = $derived(team?.Sections ?? []);
</script>

{#if sections.length}
  <section class="numbers recap" aria-label="Marketing and social">
    <header class="row spread"><h2>Marketing and social</h2></header>
    <div class="numgrid">
      {#each sections as sec (sec.Key)}
        <div class={'numgroup g-' + sec.Key}>
          <h3 class="grouptitle">{sec.Title}<a class="chip" href={sec.Href}>Open</a></h3>
          {#if sec.Notice}<p class="groupnote">{sec.Notice}</p>{/if}
          {#if sec.Tiles?.length}
            <div class="stats" data-n={sec.Tiles.length}>
              {#each sec.Tiles as t (t.Label)}
                <a class={'stat ' + (t.Tone ?? '')} href={t.Href} title={t.Exact || undefined}>
                  <dt>{t.Label}</dt>
                  <dd>{t.Value}</dd>
                  <small class="period">{t.Period}</small>
                  {#if t.Detail}<small class="period detail">{t.Detail}</small>{/if}
                </a>
              {/each}
            </div>
          {/if}
          {#if sec.Rows?.length}
            <ul class="plain rows">
              {#each sec.Rows as r (r.Title + r.Detail)}
                <li><a href={r.Href}>{r.Title}</a>{#if r.Detail}<span class="muted small">{r.Detail}</span>{/if}</li>
              {/each}
            </ul>
          {/if}
        </div>
      {/each}
    </div>
  </section>
{/if}

<style>
  .recap { margin: 0; }
  .recap header { margin-bottom: var(--s-2); }
  .recap h2 { margin: 0; }
  .groupnote { margin: 0 0 var(--s-2); color: var(--muted); font-size: var(--t-xs); line-height: 1.35; }
  .detail { display: block; }
  .rows { margin: var(--s-3) 0 0; display: grid; gap: var(--s-1); }
  .rows li { display: flex; gap: var(--s-3); justify-content: space-between; align-items: baseline; padding: var(--s-1) 0; border-bottom: 1px solid var(--line); }
  .rows li a { min-width: 0; overflow-wrap: anywhere; }
  .rows li .muted { flex: none; text-align: right; }
</style>
