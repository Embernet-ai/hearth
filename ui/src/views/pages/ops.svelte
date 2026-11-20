<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Background work</h1>
  {#if F.t(dot?.Message)}<p  class="err">{dot?.Message}</p>{/if}

  {#if F.t(F.not(dot?.WorkerOn))}
    <p  class="warn">
      This replica is not running jobs. The queue below will not move unless
      another one is.
    </p>
  {/if}

  {#if F.t(dot?.Stats)}{@const w1 = dot?.Stats}
    <div  class="stats">
      
      <div  class={"stat " + S((F.t(F.gt(w1?.OldestUnclaimed, 60000000000)) ? ("bad") : ""))}>
        <dt>Oldest waiting</dt>
        <dd>{#if F.t(w1?.Ready)}{F.duration(w1?.OldestUnclaimed)}{:else}nothing waiting{/if}</dd>
      </div>
      <div  class="stat"><dt>Ready</dt><dd>{w1?.Ready}</dd></div>
      <div  class="stat"><dt>In flight</dt><dd>{w1?.Claimed}</dd></div>
      <div  class="stat"><dt>Pending</dt><dd>{w1?.Pending}</dd></div>
      <div  class={"stat " + S((F.t(w1?.Dead) ? ("bad") : ""))}><dt>Dead</dt><dd>{w1?.Dead}</dd></div>
      <div  class={"stat " + S((F.t(w1?.Unpublished) ? ("warnish") : ""))}>
        <dt>Unpublished events</dt><dd>{w1?.Unpublished}</dd>
      </div>
    </div>

    {#if F.t(w1?.ByKind)}
      <h3>By kind</h3>
      <table  class="list">
        <thead><tr><th>Kind</th><th>Pending</th><th>Ready</th></tr></thead>
        <tbody>
          {#each F.rng(w1?.ByKind) as [k2, d2]}
            <tr><td><code>{d2?.Kind}</code></td><td>{d2?.Pending}</td><td>{d2?.Ready}</td></tr>
          {/each}
        </tbody>
      </table>
    {/if}
  {/if}

  <h3>What events turn into</h3>
  {#if F.t(dot?.Routes)}
    <table  class="list">
      <thead><tr><th>Event</th><th>On</th><th>Becomes</th></tr></thead>
      <tbody>
        {#each F.rng(dot?.Routes) as [k3, d3]}
          <tr>
            <td>{#if F.t(d3?.Event)}<code>{d3?.Event}</code>{:else}<span  class="muted">anything</span>{/if}</td>
            <td>{#if F.t(d3?.EntityType)}<code>{d3?.EntityType}</code>{:else}<span  class="muted">anything</span>{/if}</td>
            <td><code>{d3?.JobKind}</code></td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted small">
      Nothing subscribes yet. Events are still recorded and published, which is
      what lets a subscriber added later be backfilled from the history rather
      than starting from the day it was written.
    </p>
  {/if}

  <h3>Search index</h3>
  <div  class="stats">
    {#each F.rng(dot?.Engines) as [k4, d4]}{@const v_name = k4}{@const v_err = d4}
      <div  class={"stat " + S((F.t(v_err) ? ("bad") : ""))}>
        <dt>{v_name}</dt>
        <dd>{#if F.t(v_err)}not answering{:else}ready{/if}</dd>
      </div>
    {/each}
    {#if F.t(dot?.IndexState)}{@const w5 = dot?.IndexState}
      <div  class="stat"><dt>Indexed</dt><dd>{w5?.Indexed}</dd></div>
      
      <div  class={"stat " + S((F.t(w5?.Stale) ? ("warnish") : ""))}><dt>Stale</dt><dd>{w5?.Stale}</dd></div>
      <div  class={"stat " + S((F.t(w5?.Failed) ? ("bad") : ""))}><dt>Failed</dt><dd>{w5?.Failed}</dd></div>
      <div  class="stat"><dt>Awaiting text</dt><dd>{w5?.AwaitingExtraction}</dd></div>
    {/if}
  </div>
  {#each F.rng(dot?.Engines) as [k6, d6]}{@const v_name = k6}{@const v_err = d6}
    {#if F.t(v_err)}<p  class="err">{v_name}: {v_err}</p>{/if}
  {/each}
  <p  class="muted small">
    Postgres is the floor and cannot be down while this page is loading, so
    search always answers. A cluster that is not answering makes results
    narrower, not absent.
  </p>
  <form  method="post"  action="/ops/search/backfill">
    <button  type="submit">Rebuild the index from Postgres</button>
  </form>

  <h3>Dead letters</h3>
  {#if F.t(dot?.Dead)}
    <table  class="list">
      <thead><tr><th>Kind</th><th>Attempts</th><th>Failed</th><th>Error</th><th></th></tr></thead>
      <tbody>
        {#each F.rng(dot?.Dead) as [k7, d7]}
          <tr>
            <td><code>{d7?.Kind}</code></td>
            <td>{d7?.Attempts}</td>
            <td  class="muted">{F.ago(d7?.FailedAt)}</td>
            <td  class="muted small error-cell">{F.firstLine(d7?.Error)}</td>
            <td  class="row">
              
              <form  method="post"  action={"/ops/jobs/dead/" + S(d7?.ID) + "/revive"}  class="inline">
                <button  type="submit"  class="ghost small">Retry</button>
              </form>
              <form  method="post"  action={"/ops/jobs/dead/" + S(d7?.ID) + "/discard"}  class="inline">
                <button  type="submit"  class="ghost small">Discard</button>
              </form>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted small">Nothing has given up.</p>
  {/if}

  <h3>Self test</h3>
  <p  class="muted small">
    Enqueues a job that fails on purpose, twice, and then lands in the dead
    letter table above. A mechanism whose whole job is to handle failure is not
    trustworthy until it has been watched failing on the deployment that is
    actually running.
  </p>
  <form  method="post"  action="/ops/jobs/selftest">
    <button  type="submit">Run the self test</button>
  </form>
</section>
{/snippet}
{@render content(dot)}
