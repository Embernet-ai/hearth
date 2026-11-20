<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Canvas</h1>
  <p  class="muted small">
    A spatial surface for the things a list cannot hold: a line diagram, a
    floor plan, a decision somebody sketched in a meeting. Every card on one is
    a live entity rather than a rectangle with a word in it, which is what
    stops it becoming a picture that goes out of date the day it is drawn.
  </p>

  {#if F.t(dot?.Canvases)}
    <ul  class="files"  data-canvases>
      {#each F.rng(dot?.Canvases) as [k1, d1]}
        <li  data-canvas={S(d1?.ID)}>
          <a  href={("/canvas/" + S(d1?.ID))}>{d1?.Name}</a>
          <span  class="muted small">{d1?.Nodes} card{#if F.t(F.ne(d1?.Nodes, 1))}s{/if}</span>
          <span  class="muted small">{F.ago(d1?.UpdatedAt)}</span>
          <form  method="post"  action={"/canvas/" + S(d1?.ID) + "/delete"}  class="inline">
            <button  type="submit"  class="ghost small">Remove</button>
          </form>
        </li>
      {/each}
    </ul>
  {:else}
    <p  class="muted">Nothing drawn yet.</p>
  {/if}

  <form  method="post"  action="/canvas"  class="row wrap">
    <input  name="name"  placeholder="Line 3 layout"  required aria-label="Name">
    <select  name="space"  aria-label="Space">
      <option  value="">No space</option>
      {#each F.rng(dot?.Spaces) as [k2, d2]}<option  value={S(d2?.ID)}>{d2?.Path}</option>{/each}
    </select>
    <button  type="submit">New canvas</button>
  </form>
</section>
{/snippet}
{@render content(dot)}
