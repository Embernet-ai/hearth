<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  import { bulkbar, pager, groupselect, grouprow, crmviewsbar } from './shared.svelte';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <div  class="row spread">
    <h1>Companies</h1>
    <form  class="row"  method="get"  action="/companies">
      <input  name="q"  value={S(dot?.SearchText)}  placeholder="Name or domain"  aria-label="Search">
      <select  name="kind"  aria-label="Kind">
        <option  value="">Any kind</option>
        {#each F.rng(dot?.CompanyKinds) as [k1, d1]}
          <option  value={S(d1)}  {...(F.t(F.eq(d1, dot?.Filter)) ? {"selected": true} : {})}>{d1}</option>
        {/each}
      </select>
      <button  type="submit"  class="ghost small">Filter</button>
    </form>
  </div>
  {#if F.t(dot?.SalesOn)}
  <form  class="row wrap"  method="get"  action="/companies"  data-company-filters>
    <label  class="muted small">Owner
      <select  name="owner"  aria-label="Owner filter">
        <option  value="">Anyone</option>
        <option  value="none"  {...(F.t(F.eq(dot?.OwnerSel, "none")) ? {"selected": true} : {})}>Nobody</option>
        {#each F.rng(dot?.Owners) as [k2, d2]}<option  value={S(d2?.PrincipalID)}  {...(F.t(F.eq(d2?.PrincipalID, dot?.OwnerSel)) ? {"selected": true} : {})}>{d2?.DisplayName}</option>{/each}
      </select></label>
    <label  class="muted small">Type
      <select  name="type"  aria-label="Type filter">
        <option  value="">Any</option>
        <option  value="none"  {...(F.t(F.eq(dot?.TypeSel, "none")) ? {"selected": true} : {})}>No type yet</option>
        {#each F.rng(dot?.CompanyTypes) as [k3, d3]}<option  value={S(d3?.Key)}  {...(F.t(F.eq(d3?.Key, dot?.TypeSel)) ? {"selected": true} : {})}>{d3?.Label}</option>{/each}
      </select></label>
    <label  class="muted small">Tag
      <input  name="tag"  value={S(dot?.TagSel)}  list="company-tags"  placeholder="any tag"  style="width:9rem"  aria-label="Tag filter"></label>
    <datalist  id="company-tags">{#each F.rng(dot?.TagList) as [k4, d4]}<option  value={S(d4)}></option>{/each}</datalist>
    <input  type="hidden"  name="q"  value={S(dot?.SearchText)}>
    <input  type="hidden"  name="kind"  value={S(dot?.Filter)}>
    {@render groupselect(dot)}
    <button  type="submit"  class="ghost small">Apply</button>
    {#if F.t(dot?.CanManageTypes)}<a  class="chip"  href={("/crm/types")}>Edit the types</a>{:else}<a  class="chip"  href={("/crm/types")}>Types</a>{/if}
    <a  class="chip"  href={(S(dot?.ExportURL))}>Export CSV</a>
    <a  class="chip"  href={("/crm/import")}>Import CSV</a>
    <a  class="chip"  href={("/crm/favorites")}>Favorites</a>
  </form>
  {/if}
  {@render crmviewsbar(dot)}
  <p  class="muted small">
    Vendors, contractors, and manufacturers. One row per firm with a kind on it
    rather than three lists, because a company that supplies parts and also
    sends an engineer is both, and two rows means two phone numbers that drift
    apart.
  </p>

  {#if F.t(dot?.SalesOn)}
    {#if F.t(dot?.CompanyRows)}
    <table  class="list"  data-sales-list>
      <thead><tr>
        <th></th>
        <th><a  href={(S(F.index(dot?.SortLinks, "name")))}>Company</a></th>
        <th><a  href={(S(F.index(dot?.SortLinks, "kind")))}>Kind</a></th>
        <th>Types</th>
        <th>Domain</th>
        <th><a  href={(S(F.index(dot?.SortLinks, "owner")))}>Owner</a></th>
        <th>Location</th>
        <th><a  href={(S(F.index(dot?.SortLinks, "revenue")))}>Revenue</a></th>
        <th><a  href={(S(F.index(dot?.SortLinks, "contacts")))}>People</a></th>
        <th><a  href={(S(F.index(dot?.SortLinks, "deals")))}>Deals</a></th>
      </tr></thead>
      <tbody>
        {#each F.rng(dot?.CompanyRows) as [k5, d5]}
          {@render grouprow(dot, k5, 9)}
          <tr  data-company={S(d5?.ID)}>
            <td><input  type="checkbox"  name="ids"  value={S(d5?.ID)}  form="bulkform"  aria-label="Select"></td>
            <td><a  href={("/companies/" + S(d5?.ID))}>{d5?.Name}</a></td>
            <td>{#if F.t(d5?.Kind)}<span  class="tag">{d5?.Kind}</span>{/if}</td>
            <td>{#each F.rng(d5?.TypeLabels) as [k6, d6]}<span  class="tag">{d6}</span> {/each}{#each F.rng(d5?.Tags) as [k7, d7]}<span  class="tag muted">#{d7}</span> {/each}</td>
            <td  class="muted small">{d5?.Domain}</td>
            <td  class="muted small">{d5?.OwnerName}</td>
            <td  class="muted small">{d5?.City}{#if F.t(F.and(d5?.City, d5?.State))}, {/if}{d5?.State}</td>
            <td  class="muted small">{d5?.Revenue}</td>
            <td  class="muted"  data-contacts={S(d5?.Contacts)}>{d5?.Contacts}</td>
            <td  class="muted">{d5?.Deals}</td>
          </tr>
        {/each}
      </tbody>
    </table>
    {@render pager(dot?.Pager)}
    {@render bulkbar(dot, "company")}
    {:else}
      <p  class="muted">Nothing here yet.</p>
    {/if}
  {:else if F.t(dot?.Companies)}
    <table  class="list">
      <thead><tr><th>Company</th><th>Kind</th><th>Domain</th><th>Contacts</th></tr></thead>
      <tbody>
        {#each F.rng(dot?.Companies) as [k8, d8]}
          <tr  data-company={S(d8?.ID)}>
            <td><a  href={("/companies/" + S(d8?.ID))}>{d8?.Name}</a></td>
            <td><span  class="tag">{d8?.Kind}</span></td>
            <td  class="muted small">{d8?.Domain}</td>
            <td  class="muted"  data-contacts={S(d8?.Contacts)}>{d8?.Contacts}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">Nothing here yet.</p>
  {/if}

  <h2>Add a company</h2>
  <form  method="post"  action="/companies"  class="row wrap">
    <input  name="name"  placeholder="Baldor Electric"  required aria-label="Name">
    <select  name="kind"  aria-label="Kind">
      {#each F.rng(dot?.CompanyKinds) as [k9, d9]}<option  value={S(d9)}>{d9}</option>{/each}
    </select>
    <input  name="domain"  placeholder="baldor.com"  style="width:12rem"  aria-label="Domain">
    <input  name="notes"  placeholder="notes"  aria-label="Notes">
    <button  type="submit">Add</button>
  </form>
</section>
{/snippet}
{@render content(dot)}
