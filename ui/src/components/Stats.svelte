<script lang="ts">
  // The numbers on the dashboard: one person's hours, work and deals. Every tile is a query the server ran over live records
  // (internal/web/statspanel.go), says which period it covers, and links to the records it counts. Somebody who may see
  // colleagues' hours gets a picker; everybody else sees their own and nothing to pick.
  interface Tile { Label: string; Value: string; Detail?: string; Period: string; Href: string; Tone?: string; Percent?: number; HasPercent?: boolean; Of?: string; Note?: string }
  interface Bar { Label: string; Value: string; Percent: number; Href: string; Current?: boolean }
  interface Stats {
    Person: { ID: string; Name: string };
    Self: boolean;
    People?: { ID: string; Name: string }[] | null;
    Groups?: { Key: string; Title: string; Tiles: Tile[] }[] | null;
    Weeks?: Bar[] | null;
    WeeksTarget?: string;
    Stages?: Bar[] | null;
    Recent?: { Kind: string; Title: string; Detail?: string; When?: string; Href: string }[] | null;
  }
  let { stats }: { stats: Stats | null | undefined } = $props();
  const groups = $derived(stats?.Groups ?? []);
  const weeks = $derived(stats?.Weeks ?? []);
  const stages = $derived(stats?.Stages ?? []);
  const recent = $derived(stats?.Recent ?? []);
  const people = $derived(stats?.People ?? []);
</script>

{#if stats}
  <section class="numbers" aria-label="Numbers">
    <header class="row spread">
      <h2>{stats.Self ? 'My numbers' : `Numbers for ${stats.Person.Name}`}</h2>
      {#if people.length > 1}
        <form method="get" action="/" class="inline row">
          <label class="small muted" for="statsperson">Person</label>
          <select id="statsperson" name="person" onchange={(e) => e.currentTarget.form?.requestSubmit()}>
            {#each people as p (p.ID)}<option value={p.ID} selected={p.ID === stats.Person.ID}>{p.Name}</option>{/each}
          </select>
        </form>
      {/if}
    </header>

    <div class="numgrid">
    {#each groups as g (g.Key)}
      <div class={'numgroup g-' + g.Key}>
      <h3 class="grouptitle">{g.Title}</h3>
      <div class="stats" style:--cols={g.Tiles.length > 4 ? 3 : g.Tiles.length}>
        {#each g.Tiles as t, i (t.Label)}
          <a class={'stat ' + (t.Tone ?? '')} href={t.Href}>
            <dt>{t.Label}</dt>
            <dd>{t.Value}{#if t.Of}<span class="of"> of {t.Of}</span>{/if}</dd>
            {#if t.HasPercent}
              <div class="meter" role="img" aria-label={`${t.Percent} percent of ${t.Of}`}>
                <span class:over={(t.Percent ?? 0) > 100} style:width={`${Math.min(t.Percent ?? 0, 100)}%`}></span>
              </div>
            {/if}
            <small class="period">{t.Period}{#if t.Detail}<span class="detail"> &middot; {t.Detail}</span>{/if}</small>
            {#if t.Note && g.Tiles.findIndex((x) => x.Note === t.Note) === i}<small class="note">{t.Note}</small>{/if}
          </a>
        {/each}
      </div>
      </div>
    {/each}

    {#if weeks.length}
      <div class="numgroup numweeks">
      <h3 class="grouptitle">Hours, last eight weeks <span class="muted small">against {stats.WeeksTarget} a week</span></h3>
      <ol class="weeks">
        {#each weeks as w (w.Href)}
          <li class:current={w.Current}>
            <a href={w.Href} title={`Week of ${w.Label}: ${w.Value}`}>
              <span class="col"><span class="fill" style:height={`${Math.max(w.Percent, w.Value === '0m' ? 0 : 3)}%`}></span></span>
              <span class="val">{w.Value}</span>
              <span class="lab">{w.Label}</span>
            </a>
          </li>
        {/each}
      </ol>
      </div>
    {/if}

    {#if stages.length}
      <div class="numgroup numstages">
      <h3 class="grouptitle">Open pipeline by stage</h3>
      <ul class="stagebars">
        {#each stages as s (s.Label)}
          <li>
            <a href={s.Href}>
              <span class="name">{s.Label}</span>
              <span class="track"><span style:width={`${s.Percent}%`}></span></span>
              <span class="val">{s.Value}</span>
            </a>
          </li>
        {/each}
      </ul>
      </div>
    {/if}

    {#if recent.length}
      <div class="numgroup numrecent">
      <h3 class="grouptitle">Recently closed</h3>
      <ul class="plain recent">
        {#each recent as r (r.Href)}
          <li><span class="tag">{r.Kind}</span> <a href={r.Href}>{r.Title}</a>{#if r.Detail} <span class="muted small">{r.Detail}</span>{/if}{#if r.When} <span class="muted small">{r.When}</span>{/if}</li>
        {/each}
      </ul>
      </div>
    {/if}
    </div>
  </section>
{/if}

<style>
  .numbers { margin: 0 0 var(--s-5); }
  .numbers header { margin-bottom: var(--s-2); gap: var(--s-3); }
  .numbers h2 { margin: 0; }
  .numbers header form { gap: var(--s-2); }
  .numbers header select { min-width: 12rem; }
  .grouptitle { margin: var(--s-4) 0 var(--s-2); }
  .grouptitle .muted { text-transform: none; letter-spacing: 0; font-weight: 400; margin-left: var(--s-2); }
  .period { display: block; }
  .flat { margin: 0 0 var(--s-3); }
  .detail { white-space: nowrap; }
  .note { display: block; margin-top: var(--s-1); color: var(--muted); font-size: var(--t-2xs); line-height: 1.35; }
  .meter { height: 4px; margin-top: var(--s-1); background: var(--hover); border-radius: 2px; overflow: hidden; }
  .meter span { display: block; height: 100%; background: var(--accent-dim); }
  .meter span.over { background: var(--warn); }
  .weeks { list-style: none; margin: 0; padding: 0; display: grid; grid-template-columns: repeat(8, minmax(0, 1fr)); gap: var(--s-2); }
  .weeks a { display: grid; justify-items: center; gap: var(--s-1); text-decoration: none; color: var(--fg); }
  .weeks .col { display: flex; align-items: flex-end; width: 100%; height: 3.25rem; border-bottom: 1px solid var(--line-strong); }
  .weeks .fill { display: block; width: 100%; background: var(--edge); border-radius: 3px 3px 0 0; }
  .weeks li.current .fill { background: var(--accent-dim); }
  .weeks .val { font-size: var(--t-xs); font-variant-numeric: tabular-nums; }
  .weeks .lab { font-size: var(--t-2xs); color: var(--muted); }
  .stagebars { list-style: none; margin: 0; padding: 0; display: grid; gap: var(--s-1); }
  .stagebars a { display: grid; grid-template-columns: 6rem 1fr minmax(7rem, auto); gap: var(--s-3); align-items: center; text-decoration: none; color: var(--fg); font-size: var(--t-sm); }
  .stagebars .track { height: 6px; background: var(--hover); border-radius: 3px; overflow: hidden; }
  .stagebars .track span { display: block; height: 100%; background: var(--edge); }
  .stagebars .val { font-size: var(--t-xs); color: var(--muted); text-align: right; font-variant-numeric: tabular-nums; }
  .recent li { margin: var(--s-1) 0; }
  @media (max-width: 40rem) {
    .weeks { grid-template-columns: repeat(4, minmax(0, 1fr)); row-gap: var(--s-3); }
    .stagebars a { grid-template-columns: 4.5rem minmax(0, 1fr) auto; gap: var(--s-2); }
    .stagebars .val { text-align: right; white-space: nowrap; }
  }
</style>
