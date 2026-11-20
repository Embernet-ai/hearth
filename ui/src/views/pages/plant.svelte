<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Plant</h1>
  <p  class="muted small">
    Site, line, machine, component. Every number on a row is that asset plus
    everything below it, because the person deciding whether to run the line
    tonight is asking about the line and the answer is made of things four
    levels down.
  </p>

  {#if F.t(dot?.Assets)}
    <table  class="list">
      <thead>
        <tr>
          <th>Asset</th><th>Code</th><th>Criticality</th>
          <th>Open work</th><th>Time</th><th>Cost</th><th>Downtime</th><th></th>
        </tr>
      </thead>
      <tbody>
        {#each F.rng(dot?.Assets) as [k1, d1]}
          <tr  data-asset={S(d1?.ID)}  data-depth={S(d1?.Depth)}                {...(F.t(d1?.Down) ? {"class": "down"} : {})}>
            <td>
              
              <span  class="tree"  style={"padding-left:" + S(F.indent(d1?.Depth))}>
                <a  href={("/documents/" + S(d1?.ID))}>{d1?.Title}</a>
              </span>
              {#if F.t(d1?.Down)}<span  class="status blocked">down</span>{/if}
              {#if F.t(d1?.Retired)}<span  class="tag">retired</span>{/if}
            </td>
            <td  class="muted small">{d1?.Code}</td>
            <td>
              {#if F.t(d1?.Criticality)}
                <span  class={"tag crit-" + S(d1?.Criticality)}>{d1?.Criticality}</span>
              {/if}
            </td>
            <td  data-open-rolled={S(d1?.RolledOpen)}>
              {d1?.RolledOpen}
              {#if F.t(F.ne(d1?.RolledOpen, d1?.OwnOpen))}
                <span  class="muted small">{d1?.OwnOpen} here</span>
              {/if}
            </td>
            <td  class="muted"  data-minutes-rolled={S(d1?.RolledMinutes)}>{F.hours(d1?.RolledMinutes)}</td>
            <td  class="muted">{#if F.t(d1?.RolledCost)}{F.cost(d1?.RolledCost)}{/if}</td>
            <td  class="muted"  data-downtime-rolled={S(d1?.RolledDowntime)}>
              {#if F.t(d1?.RolledDowntime)}{F.hours(d1?.RolledDowntime)}{/if}
            </td>
            <td>
              {#if F.t(d1?.Code)}<a  class="ghost small"  href={("/documents/" + S(d1?.ID) + "/label")}>Label</a>{/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">
      No assets yet, or none you have been granted. Create one from
      <a  href={("/assets")}>Assets</a> and give it a place in the tree.
    </p>
  {/if}
</section>
{/snippet}
{@render content(dot)}
