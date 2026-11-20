<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <div  class="row spread">
    <h1>Company types</h1>
    <a  class="chip"  href={("/companies")}>Companies</a>
  </div>
  <p  class="muted small">What a company is to us. A company can be several at once, and free tags sit beside the types.
    The accounting column says how a type lines up with EmberOps: a client is a Customer there and a vendor is a Supplier.
    {#if F.t(F.not(dot?.CanManageTypes))}An admin edits this list.{/if}</p>
  <table  class="list"  data-types>
    <thead><tr><th>Type</th><th>Key</th><th>In EmberOps</th><th>Order</th><th>State</th>{#if F.t(dot?.CanManageTypes)}<th></th>{/if}</tr></thead>
    <tbody>
      {#each F.rng(dot?.CompanyTypes) as [k1, d1]}
      <tr  data-type={S(d1?.Key)} {...(F.t(d1?.Archived) ? {"class": "muted"} : {})}>
        {#if F.t(dot?.CanManageTypes)}
          
          <td><input  type="hidden"  name="id"  value={S(d1?.ID)}  {@attach A("form", "f-" + S(d1?.ID))}><input  name="label"  value={S(d1?.Label)}  aria-label="Name"  {@attach A("form", "f-" + S(d1?.ID))}></td>
          <td  class="muted small">{d1?.Key}</td>
          <td><select  name="erp_role"  aria-label="Accounting role"  {@attach A("form", "f-" + S(d1?.ID))}>
            <option  value=""  {...(F.t(F.eq(d1?.ERPRole, "")) ? {"selected": true} : {})}>nothing</option>
            <option  value="customer"  {...(F.t(F.eq(d1?.ERPRole, "customer")) ? {"selected": true} : {})}>Customer</option>
            <option  value="supplier"  {...(F.t(F.eq(d1?.ERPRole, "supplier")) ? {"selected": true} : {})}>Supplier</option></select></td>
          <td><input  name="position"  type="number"  value={S(d1?.Position)}  style="width:4rem"  aria-label="Order"  {@attach A("form", "f-" + S(d1?.ID))}></td>
          <td><select  name="archived"  aria-label="State"  {@attach A("form", "f-" + S(d1?.ID))}><option  value="0">in use</option><option  value="1"  {...(F.t(d1?.Archived) ? {"selected": true} : {})}>archived</option></select></td>
          <td><form  id={"f-" + S(d1?.ID)}  method="post"  action="/crm/types"><button  type="submit"  class="ghost small">Save</button></form></td>
        {:else}
          <td>{d1?.Label}</td><td  class="muted small">{d1?.Key}</td>
          <td  class="muted small">{#if F.t(F.eq(d1?.ERPRole, "customer"))}Customer{:else if F.t(F.eq(d1?.ERPRole, "supplier"))}Supplier{/if}</td>
          <td  class="muted small">{d1?.Position}</td><td  class="muted small">{#if F.t(d1?.Archived)}archived{:else}in use{/if}</td>
        {/if}
      </tr>
      {/each}
    </tbody>
  </table>
  {#if F.t(dot?.CanManageTypes)}
  <h2>Add a type</h2>
  <form  method="post"  action="/crm/types"  class="row wrap"  data-new-type>
    <input  name="label"  placeholder="Distributor"  required aria-label="Name">
    <select  name="erp_role"  aria-label="Accounting role"><option  value="">no accounting role</option>
      <option  value="customer">Customer in EmberOps</option><option  value="supplier">Supplier in EmberOps</option></select>
    <button  type="submit">Add</button>
  </form>
  {/if}
</section>
{/snippet}
{@render content(dot)}
