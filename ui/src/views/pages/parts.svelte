<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <div  class="row spread">
    <h1>Parts</h1>
    <form  class="row"  method="get"  action="/parts">
      <input  name="q"  value={S(dot?.SearchText)}  placeholder="Name or part number"  aria-label="Search">
      <button  type="submit"  class="ghost small">Search</button>
    </form>
  </div>
  <p  class="muted small">
    Stock is summed from the movements rather than stored, so every number here
    has a list of rows behind it saying who moved what, when, and against which
    job.
  </p>

  {#if F.t(dot?.Parts)}
    <table  class="list">
      <thead>
        <tr><th>Part</th><th>Number</th><th>Supplier</th><th>Cost</th>
            <th>On hand</th><th>Used</th><th>Correct the count</th><th></th></tr>
      </thead>
      <tbody>
        {#each F.rng(dot?.Parts) as [k1, d1]}
          <tr  data-part={S(d1?.ID)}  {...(F.t(d1?.Low) ? {"class": "low"} : {})}>
            <td>{d1?.Name}</td>
            <td  class="muted small">{d1?.SKU}</td>
            <td  class="muted small">
              {#if F.t(d1?.SupplierID)}<a  href={("/companies/" + S(F.deref_s(d1?.SupplierID)))}>{d1?.SupplierName}</a>{/if}
            </td>
            <td  class="muted">{F.cost(d1?.UnitCostCents)}</td>
            <td  data-stock={S(F.qty(d1?.OnHand))}  class={S((F.t(d1?.Low) ? ("over") : ""))}>
              {F.qty(d1?.OnHand)}{#if F.t(d1?.Low)} <span  class="muted small">reorder</span>{/if}
            </td>
            <td  class="muted"  data-used={S(F.qty(d1?.Used))}>{F.qty(d1?.Used)}</td>
            <td>
              
              <form  method="post"  action={"/parts/" + S(d1?.ID) + "/adjust"}  class="inline row">
                <input  name="delta"  placeholder="+/-"  style="width:4.5rem"  aria-label="Change by">
                <input  name="note"  placeholder="why"  style="width:8rem"  aria-label="Why"  required>
                <button  type="submit"  class="ghost small">Apply</button>
              </form>
            </td>
            <td>
              <form  method="post"  action={"/parts/" + S(d1?.ID) + "/delete"}  class="inline">
                <button  type="submit"  class="ghost small">Remove</button>
              </form>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">No parts yet.</p>
  {/if}

  <h2>Add a part</h2>
  <form  method="post"  action="/parts"  class="row wrap">
    <input  name="name"  placeholder="Bearing, SKF 6205"  required aria-label="Name">
    <input  name="sku"  placeholder="6205-2RS"  style="width:9rem"  aria-label="Part number">
    <select  name="supplier"  aria-label="Supplier">
      <option  value="">No supplier</option>
      {#each F.rng(dot?.Companies) as [k2, d2]}<option  value={S(d2?.ID)}>{d2?.Name}</option>{/each}
    </select>
    <input  name="cost"  placeholder="12.50"  style="width:6rem"  aria-label="Unit cost">
    <input  name="reorder"  type="number"  min="0"  placeholder="reorder at"             style="width:7rem"  aria-label="Reorder point">
    <button  type="submit">Add</button>
  </form>
</section>
{/snippet}
{@render content(dot)}
