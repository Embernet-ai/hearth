<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <div  class="row spread">
    <h1>{dot?.Canvas?.Name}</h1>
    <a  class="chip"  href={("/canvas")}>All canvases</a>
  </div>

  
  <div  class="canvas"  data-canvas={S(dot?.Canvas?.ID)}>
    <svg  class="canvas-edges"  aria-hidden="true">
      {#each F.rng(dot?.Edges) as [k1, d1]}
        <line  data-edge={S(d1?.ID)}  data-from={S(d1?.From)}  data-to={S(d1?.To)}                x1="0"  y1="0"  x2="0"  y2="0"></line>
      {/each}
    </svg>
    {#each F.rng(dot?.Nodes) as [k2, d2]}
      <article  class={"node" + S((F.t(d2?.Missing) ? (" gone") : "")) + S((F.t(F.not(d2?.Visible)) ? (" hidden") : ""))}                 data-node={S(d2?.ID)}                 style={"left:" + S(F.px(d2?.X)) + ";top:" + S(F.px(d2?.Y)) + ";width:" + S(F.px(d2?.W)) + ";height:" + S(F.px(d2?.H))}>
        <header  class="row spread">
          <span  class="node-label">
            {#if F.t(d2?.Href)}<a  href={(S(d2?.Href))}>{d2?.Label}</a>{:else}{d2?.Label}{/if}
          </span>
          {#if F.t(d2?.Kind)}<span  class="tag">{d2?.Kind}</span>{/if}
        </header>
        
        {#if F.t(d2?.Status)}<span  class={"status " + S(d2?.Status)}>{F.statusLabel(d2?.Status)}</span>{/if}
        <form  method="post"  action={"/canvas/" + S(dot?.Canvas?.ID) + "/nodes/" + S(d2?.ID)}                class="row node-move">
          <input  name="x"  type="number"  value={S(d2?.X)}  aria-label="X"  style="width:5rem">
          <input  name="y"  type="number"  value={S(d2?.Y)}  aria-label="Y"  style="width:5rem">
          <input  type="hidden"  name="w"  value={S(d2?.W)}>
          <input  type="hidden"  name="h"  value={S(d2?.H)}>
          <button  type="submit"  class="ghost small">Move</button>
        </form>
        <form  method="post"  action={"/canvas/" + S(dot?.Canvas?.ID) + "/nodes/" + S(d2?.ID) + "/delete"}  class="inline">
          <button  type="submit"  class="ghost small">Remove</button>
        </form>
      </article>
    {/each}
  </div>

  <h3>Add a card</h3>
  <form  method="post"  action={"/canvas/" + S(dot?.Canvas?.ID) + "/nodes"}  class="row wrap">
    <select  name="entity"  aria-label="Entity">
      <option  value="">A plain label</option>
      {#each F.rng(dot?.Assignable) as [k3, d3]}
        <option  value={S(d3?.ID)}>{d3?.Kind} · {#if F.t(d3?.Title)}{d3?.Title}{:else}Untitled{/if}</option>
      {/each}
    </select>
    <input  name="text"  placeholder="or some text"  aria-label="Text">
    <input  name="x"  type="number"  value="40"  style="width:5rem"  aria-label="X">
    <input  name="y"  type="number"  value="40"  style="width:5rem"  aria-label="Y">
    <button  type="submit">Add</button>
  </form>

  {#if F.t(dot?.Nodes)}
    <h3>Join two cards</h3>
    <form  method="post"  action={"/canvas/" + S(dot?.Canvas?.ID) + "/edges"}  class="row wrap">
      <select  name="from"  required aria-label="From">
        {#each F.rng(dot?.Nodes) as [k4, d4]}<option  value={S(d4?.ID)}>{d4?.Label}</option>{/each}
      </select>
      <select  name="to"  required aria-label="To">
        {#each F.rng(dot?.Nodes) as [k5, d5]}<option  value={S(d5?.ID)}>{d5?.Label}</option>{/each}
      </select>
      <input  name="label"  placeholder="feeds"  style="width:8rem"  aria-label="Label">
      <button  type="submit">Join</button>
    </form>
  {/if}

  {#if F.t(dot?.Edges)}
    <ul  class="files"  data-edge-list>
      {#each F.rng(dot?.Edges) as [k6, d6]}
        <li>
          <span  class="muted small">{#if F.t(d6?.Label)}{d6?.Label}{:else}line{/if}</span>
          <form  method="post"  action={"/canvas/" + S(dot?.Canvas?.ID) + "/edges/" + S(d6?.ID) + "/delete"}  class="inline">
            <button  type="submit"  class="ghost small">Remove</button>
          </form>
        </li>
      {/each}
    </ul>
  {/if}
</section>
{/snippet}
{@render content(dot)}
