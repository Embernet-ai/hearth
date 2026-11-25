<script lang="ts">
  // @ts-nocheck
  // The GitHub page: connect with a token you made yourself, choose repositories, see what happened in them. Read only:
  // nothing on this page writes to GitHub (internal/web/githubpage.go).
  let { pg }: { pg: any; onEnv?: any } = $props();
  const v = $derived(pg?.GitHubArea);
  const n = (x: number) => (x ?? 0).toLocaleString('en-US');
</script>

<section>
  <h1>GitHub</h1>
  {#if v?.Notice}<p class="card small" data-flash>{v.Notice}</p>{/if}

  {#if !v?.Configured}
    <p class="muted" data-not-set-up>GitHub is not available on this installation.</p>
  {:else if !v.Connected}
    <div class="card stack" data-connect>
      <h2>Connect your GitHub</h2>
      <p class="small">Hearth reads your repositories to show you commits, pull requests, issues, releases and CI runs. It only reads: it never changes
        anything on GitHub. Make a token for it on GitHub (Settings, Developer settings, Fine-grained tokens) with read-only access to the repositories
        you want, and paste it here. The token is sealed in Hearth and is yours alone; disconnecting deletes it.</p>
      <form method="post" action="/github/connect" class="row">
        <input name="token" type="password" autocomplete="off" placeholder="Your GitHub token" aria-label="GitHub token" required>
        <button type="submit">Connect</button>
      </form>
    </div>
  {:else}
    <div class="row spread">
      <p class="muted" data-connected>Connected as <strong>{v.Login}</strong>. Read only.</p>
      <form method="post" action="/github/disconnect"><button type="submit" class="ghost small">Disconnect</button></form>
    </div>
    {#if v.LastError}<p class="card small" data-flash>{v.LastError}</p>{/if}

    {#if v.Total}
      <h2>The last {v.Days} days</h2>
      <div class="stats" data-n="4" data-totals>
        <div class="stat"><dt>Commits</dt><dd>{n(v.Total.Commits)}</dd><small class="period">in {v.Repos?.length} repositories</small></div>
        <div class="stat"><dt>Pull requests</dt><dd>{n(v.Total.PRsMerged)}</dd><small class="period">merged</small><small class="period detail">{n(v.Total.PRsOpened)} opened</small></div>
        <div class="stat"><dt>Issues</dt><dd>{n(v.Total.IssuesClosed)}</dd><small class="period">closed</small><small class="period detail">{n(v.Total.IssuesOpened)} opened</small></div>
        <div class="stat {v.Total.RunsFailed > 0 ? 'bad' : ''}"><dt>CI runs</dt><dd>{n(v.Total.Runs)}</dd><small class="period">{v.Total.RunsFailed} failed</small><small class="period detail">{v.Total.Runs ? Math.round((v.Total.RunsPassed * 100) / v.Total.Runs) + '% passed' : 'no runs'}</small></div>
      </div>
      <div class="tablescroll"><table class="list" data-repos>
        <thead><tr><th>Repository</th><th>Commits</th><th>PRs merged</th><th>Issues closed</th><th>Releases</th><th>CI passed</th><th>Latest release</th></tr></thead>
        <tbody>
          {#each v.Repos ?? [] as m}
            <tr>
              <td><a href={'https://github.com/' + m.Repo}>{m.Repo}</a>{#if m.Err}<div class="muted small">{m.Err}</div>{/if}</td>
              <td>{n(m.Commits)}</td><td>{n(m.PRsMerged)}</td><td>{n(m.IssuesClosed)}</td><td>{n(m.Releases)}</td>
              <td>{m.Runs ? n(m.RunsPassed) + ' of ' + n(m.Runs) : 'none'}</td><td>{m.LatestRelease || ''}</td>
            </tr>
          {/each}
        </tbody>
      </table></div>
    {:else}
      <p class="muted" data-none-chosen>Choose the repositories you want numbers for below.</p>
    {/if}

    <h2>Repositories</h2>
    <form method="post" action="/github/repos" class="stack card" data-choose>
      {#if v.Choices?.length}
        <div class="repopick">
          {#each v.Choices as c}
            <label class="row"><input type="checkbox" name="repo" value={c.Name} checked={c.Chosen}> {c.Name}{#if c.Private} <span class="tag">private</span>{/if}</label>
          {/each}
        </div>
      {/if}
      <input name="more" placeholder="Another repository, owner/name (up to twelve in all)" aria-label="Another repository" value="">
      <div class="row"><button type="submit">Save</button></div>
    </form>
  {/if}
</section>

<style>
  .repopick { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(100%, 18rem), 1fr)); gap: var(--s-2) var(--s-4); }
  .repopick label { margin: 0; overflow-wrap: anywhere; }
  .stats { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--s-3); margin: var(--s-2) 0 var(--s-5); }
  @media (min-width: 40rem) { .stats[data-n='4'] { grid-template-columns: repeat(4, minmax(0, 1fr)); } }
  .detail { display: block; }
  /* seven columns do not fit a phone at larger type: the table scrolls inside its own box, the page never does */
  .tablescroll { overflow-x: auto; max-width: 100%; }
</style>
