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
    <h1>Contacts</h1>
    <form  class="row"  method="get"  action="/contacts">
      <input  name="q"  value={S(dot?.SearchText)}  placeholder="Name, email, or phone"  aria-label="Search">
      <button  type="submit"  class="ghost small">Search</button>
    </form>
  </div>

  <div  class="cpage">
  <div  class="clist">
  {#if F.t(dot?.SalesOn)}
    <form  class="row wrap"  method="get"  action="/contacts"  data-contact-filters>
      <input  type="hidden"  name="q"  value={S(dot?.SearchText)}>
      <label  class="muted small">Owner
        <select  name="owner"  aria-label="Owner filter">
          <option  value="">Anyone</option>
          <option  value="none"  {...(F.t(F.eq(dot?.OwnerSel, "none")) ? {"selected": true} : {})}>Nobody</option>
          {#each F.rng(dot?.Owners) as [k1, d1]}<option  value={S(d1?.PrincipalID)}  {...(F.t(F.eq(d1?.PrincipalID, dot?.OwnerSel)) ? {"selected": true} : {})}>{d1?.DisplayName}</option>{/each}
        </select></label>
      {@render groupselect(dot)}
      <button  type="submit"  class="ghost small">Apply</button>
      <a  class="chip"  href={(S(dot?.ExportURL))}>Export CSV</a>
      <a  class="chip"  href={("/crm/import")}>Import CSV</a>
      <a  class="chip"  href={("/crm/favorites")}>Favorites</a>
    </form>
    {@render crmviewsbar(dot)}
    {#if F.t(dot?.ContactRows)}
    <table  class="list"  data-sales-list>
      <thead><tr>
        <th></th>
        <th><a  href={(S(F.index(dot?.SortLinks, "name")))}>Name</a></th>
        <th><a  href={(S(F.index(dot?.SortLinks, "company")))}>Company</a></th>
        <th><a  href={(S(F.index(dot?.SortLinks, "title")))}>Title</a></th>
        <th><a  href={(S(F.index(dot?.SortLinks, "email")))}>Email</a></th>
        <th>Phone</th>
        <th><a  href={(S(F.index(dot?.SortLinks, "owner")))}>Owner</a></th>
        <th class="col-md">Updated</th>
        <th class="col-lg">LinkedIn</th>
        <th class="col-lg">Notes</th>
      </tr></thead>
      <tbody>
        {#each F.rng(dot?.ContactRows) as [k2, d2]}
          {@render grouprow(dot, k2, 9)}
          <tr  data-contact={S(d2?.ID)}>
            <td><input  type="checkbox"  name="ids"  value={S(d2?.ID)}  form="bulkform"  aria-label="Select"></td>
            <td><a  href={("/contacts/" + S(d2?.ID))}>{d2?.Name}</a></td>
            <td  class="muted small">{#if F.t(d2?.CompanyID)}<a  href={("/companies/" + S(F.deref_s(d2?.CompanyID)))}>{d2?.CompanyName}</a>{/if}</td>
            <td  class="muted small">{d2?.Title}</td>
            <td  class="muted small">{#if F.t(d2?.Email)}<a  href={("mailto:" + S(d2?.Email))}>{d2?.Email}</a>{/if}</td>
            <td  class="muted small">{d2?.Phone}</td>
            <td  class="muted small">{d2?.OwnerName}</td>
            <td  class="muted small col-md">{#if F.t(d2?.UpdatedAt)}{F.date(d2?.UpdatedAt)}{/if}</td>
            <td  class="muted small col-lg">{#if /^https?:\/\//i.test(S(d2?.LinkedIn))}<a  href={(S(d2?.LinkedIn))}  rel="noopener noreferrer">LinkedIn</a>{:else}{d2?.LinkedIn}{/if}</td>
            <td  class="muted small col-lg notecell"  title={S(d2?.Notes)}>{d2?.Notes}</td>
          </tr>
        {/each}
      </tbody>
    </table>
    {@render pager(dot?.Pager)}
    {@render bulkbar(dot, "contact")}
    {:else}
      <p  class="muted">Nobody on file yet.</p>
    {/if}
  {:else if F.t(dot?.Contacts)}
    <table  class="list">
      <thead><tr><th>Name</th><th>Company</th><th>Title</th><th>Email</th><th>Phone</th><th class="col-md">Updated</th><th class="col-lg">Notes</th></tr></thead>
      <tbody>
        {#each F.rng(dot?.Contacts) as [k3, d3]}
          <tr  data-contact={S(d3?.ID)}>
            <td><a  href={("/contacts/" + S(d3?.ID))}>{d3?.Name}</a></td>
            <td  class="muted small">
              {#if F.t(d3?.CompanyID)}<a  href={("/companies/" + S(F.deref_s(d3?.CompanyID)))}>{d3?.CompanyName}</a>{/if}
            </td>
            <td  class="muted small">{d3?.Title}</td>
            <td  class="muted small">{#if F.t(d3?.Email)}<a  href={("mailto:" + S(d3?.Email))}>{d3?.Email}</a>{/if}</td>
            <td  class="muted small">{d3?.Phone}</td>
            <td  class="muted small col-md">{#if F.t(d3?.UpdatedAt)}{F.date(d3?.UpdatedAt)}{/if}</td>
            <td  class="muted small col-lg notecell"  title={S(d3?.Notes)}>{d3?.Notes}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">Nobody on file yet.</p>
  {/if}

  </div>

  <aside  class="cadd card">
  <h2>Add a contact</h2>
  <form  method="post"  action="/contacts"  class="row wrap">
    <input  name="name"  placeholder="Name"  required aria-label="Name">
    <select  name="company"  aria-label="Company">
      <option  value="">No company</option>
      {#each F.rng(dot?.Companies) as [k4, d4]}<option  value={S(d4?.ID)}>{d4?.Name}</option>{/each}
    </select>
    <input  name="title"  placeholder="Service manager"  aria-label="Title">
    <input  name="email"  type="email"  placeholder="name@example.com"  aria-label="Email">
    <input  name="phone"  placeholder="Phone"  style="width:9rem"  aria-label="Phone">
    <button  type="submit">Add</button>
  </form>
  <p  class="muted small">
    A contact with no company is fine. The electrician who answers his own phone
    is a person and not a firm, and inventing a one-person company for him
    produces a directory full of rows nobody wanted.
  </p>
  </aside>
  </div>
</section>
{/snippet}
{@render content(dot)}
