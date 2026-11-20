<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <div  class="row spread">
    <h1>History</h1>
    <a  href={("/documents/" + S(dot?.Doc?.ID))}>Back to {#if F.t(dot?.Doc?.Title)}{dot?.Doc?.Title}{:else}the document{/if}</a>
  </div>
  <p  class="muted small">
    Every edit has written a full snapshot since the beginning. Restoring one
    writes a new version rather than rewinding the counter, because undoing an
    edit is itself an edit and the record of it matters.
  </p>

  {#if F.t(dot?.Diff)}
    <h3>
      {#if F.t(dot?.DiffFrom?.Version)}Version {dot?.DiffFrom?.Version} to {dot?.DiffTo?.Version}
      {:else}Version {dot?.DiffTo?.Version}, as first written{/if}
    </h3>
    <div  class="diff">
      {#each F.rng(dot?.Diff) as [k1, d1]}
        <div  class={"dl " + S((F.t(F.eq(d1?.Kind, "+")) ? ("add") : (F.t(F.eq(d1?.Kind, "-")) ? ("del") : "")))}><span            class="ln">{#if F.t(d1?.Old)}{d1?.Old}{/if}</span><span            class="ln">{#if F.t(d1?.New)}{d1?.New}{/if}</span><span            class="dk">{d1?.Kind}</span><span  class="dt">{d1?.Text}</span></div>
      {/each}
    </div>
  {/if}

  <table  class="list">
    <thead><tr><th>Version</th><th>Title</th><th>By</th><th>When</th><th></th></tr></thead>
    <tbody>
      {#each F.rng(dot?.History) as [k2, d2]}
        <tr>
          <td>
            <a  href={("/documents/" + S(dot?.Doc?.ID) + "/history?v=" + S(d2?.Version))}>{d2?.Version}</a>
            {#if F.t(d2?.Current)}<span  class="tag">current</span>{/if}
          </td>
          <td>{#if F.t(d2?.Title)}{d2?.Title}{:else}Untitled{/if}</td>
          <td  class="muted">{d2?.AuthorName}</td>
          <td  class="muted">{F.ago(d2?.CreatedAt)}</td>
          <td>
            {#if F.t(F.not(d2?.Current))}
              <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/restore/" + S(d2?.Version)}                      class="inline"                      data-confirm={"Restore version " + S(d2?.Version) + "? It becomes a new version, so nothing is lost."}>
                <button  type="submit"  class="ghost small">Restore</button>
              </form>
            {/if}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</section>
{/snippet}
{@render content(dot)}
