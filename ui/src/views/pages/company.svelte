<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  import { timeline, addactivity, ownerselect, favstar } from './shared.svelte';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<article  class="card">
  <div  class="row spread">
    <h1>{dot?.Company?.Name}</h1>
    <div  class="row">
      <span  class="tag">{dot?.Company?.Kind}</span>
      {@render favstar(dot, "company", dot?.Company?.ID)}
      <a  class="chip"  href={("/companies")}>All companies</a>
    </div>
  </div>

  {#if F.t(dot?.EmberOps)}
  <p  class="row wrap small"  data-emberops>In EmberOps: {#each F.rng(dot?.EmberOps) as [ke, de]}<a  class="chip"  href={S(de?.URL)}  target="_blank"  rel="noopener">{de?.Label}</a>{/each}</p>
  {/if}
  <form  class="fields"  method="post"  action={"/companies/" + S(dot?.Company?.ID)}>
    <label  for="name">Name</label>
    <input  id="name"  name="name"  value={S(dot?.Company?.Name)}  required>
    <label  for="kind">Kind</label>
    <select  id="kind"  name="kind">
      {#each F.rng(dot?.CompanyKinds) as [k1, d1]}
        <option  value={S(d1)}  {...(F.t(F.eq(d1, dot?.Company?.Kind)) ? {"selected": true} : {})}>{d1}</option>
      {/each}
    </select>
    <label  for="domain">Domain</label>
    <input  id="domain"  name="domain"  value={S(dot?.Company?.Domain)}  placeholder="baldor.com">
    <label  for="notes">Notes</label>
    <textarea  id="notes"  name="notes"  rows="3">{dot?.Company?.Notes}</textarea>
    <div  class="row">
      <button  type="submit"  class="primary">Save</button>
    </div>
  </form>

  {#if F.t(dot?.SalesOn)}
  <h3>Sales</h3>
  <form  class="fields"  method="post"  action={"/companies/" + S(dot?.Company?.ID) + "/sales"}  data-sales-details>
    <label>Owner</label>
    {@render ownerselect(F.dict("Name", "owner", "People", dot?.Owners, "Selected", dot?.Details?.OwnerID))}
    <label  for="website">Website</label>
    <input  id="website"  name="website"  value={S(dot?.Details?.Website)}  placeholder="https://baldor.com">
    <label  for="linkedin">LinkedIn</label>
    <input  id="linkedin"  name="linkedin"  value={S(dot?.Details?.LinkedIn)}>
    <label  for="tax_id">Tax ID (EIN)</label>
    <input  id="tax_id"  name="tax_id"  value={S(dot?.Details?.TaxID)}  maxlength="32"  placeholder="12-3456789">
    <label  for="street">Street</label>
    <input  id="street"  name="street"  value={S(dot?.Details?.Street)}>
    <label  for="city">City</label>
    <input  id="city"  name="city"  value={S(dot?.Details?.City)}>
    <label  for="state">State</label>
    <input  id="state"  name="state"  value={S(dot?.Details?.State)}>
    <label  for="postcode">Postcode</label>
    <input  id="postcode"  name="postcode"  value={S(dot?.Details?.Postcode)}>
    <label  for="country">Country</label>
    <input  id="country"  name="country"  value={S(dot?.Details?.Country)}>
    <input  type="hidden"  name="labels"  value="1">
    <label>Types</label>
    <div  class="row wrap"  data-company-types>
      {#each F.rng(dot?.CompanyTypes) as [k2, d2]}
        <label  class="small"><input  type="checkbox"  name="types"  value={S(d2?.Key)}  {...F.attrs(((dot) => (((v_k) => ((F.t(dot?.Labels) ? (F.rng(dot?.Labels?.Types).map(([k3, d3]) => {  return ((F.t(F.eq(d3?.Key, v_k)) ? ("checked") : "")); }).join('')) : "")))(dot?.Key)))(F.dict("Key", d2?.Key, "Labels", dot?.Labels)))}> {d2?.Label}</label>
      {/each}
    </div>
    <label  for="tags">Tags</label>
    <input  id="tags"  name="tags"  value={S((F.t(dot?.Labels) ? (F.rng(dot?.Labels?.Tags).map(([k4, d4]) => { const v_i = k4; const v_t = d4; return ((F.t(v_i) ? (", ") : "") + S(v_t)); }).join('')) : ""))}  list="company-tags"  placeholder="comma separated">
    <datalist  id="company-tags">{#each F.rng(dot?.TagList) as [k5, d5]}<option  value={S(d5)}></option>{/each}</datalist>
    <label  for="revenue">Annual revenue</label>
    <input  id="revenue"  name="revenue"  value={S(F.dollars(dot?.Details?.RevenueCents))}  placeholder="2500000">
    <div  class="row"><button  type="submit"  class="primary">Save sales details</button></div>
  </form>

  <h3>Deals</h3>
  {#if F.t(dot?.Deals)}
    <ul  class="files"  data-deals>
      {#each F.rng(dot?.Deals) as [k6, d6]}
        <li><a  href={("/deals/" + S(d6?.ID))}>{d6?.Name}</a> <span  class="tag">{F.stageName(d6?.Stage)}</span>
          <span  class="muted small">{d6?.Amount}</span>{#if F.t(d6?.OwnerName)} <span  class="muted small">{d6?.OwnerName}</span>{/if}</li>
      {/each}
    </ul>
  {:else}
    <p  class="muted small">No deals with this company.</p>
  {/if}
  <form  method="post"  action="/deals"  class="row wrap">
    <input  type="hidden"  name="company"  value={S(dot?.Company?.ID)}>
    <input  name="name"  placeholder="Deal name"  required aria-label="Deal name">
    <input  name="amount"  placeholder="Amount"  style="width:8rem"  aria-label="Amount">
    <button  type="submit">Add a deal</button>
  </form>

  {@render timeline(dot)}
  {@render addactivity(F.dict("Type", "company", "ID", dot?.Company?.ID))}
  {/if}

  <h3>People</h3>
  {#if F.t(dot?.Contacts)}
    <ul  class="files"  data-people>
      {#each F.rng(dot?.Contacts) as [k7, d7]}
        <li>
          <a  href={("/contacts/" + S(d7?.ID))}>{d7?.Name}</a>
          {#if F.t(d7?.Title)}<span  class="muted small">{d7?.Title}</span>{/if}
          {#if F.t(d7?.Email)}<a  class="muted small"  href={("mailto:" + S(d7?.Email))}>{d7?.Email}</a>{/if}
          {#if F.t(d7?.Phone)}<span  class="muted small">{d7?.Phone}</span>{/if}
        </li>
      {/each}
    </ul>
  {:else}
    <p  class="muted small">Nobody on file here yet.</p>
  {/if}
  <form  method="post"  action="/contacts"  class="row wrap">
    <input  type="hidden"  name="company"  value={S(dot?.Company?.ID)}>
    <input  name="name"  placeholder="Name"  required aria-label="Name">
    <input  name="title"  placeholder="Service manager"  aria-label="Title">
    <input  name="email"  type="email"  placeholder="name@example.com"  aria-label="Email">
    <input  name="phone"  placeholder="Phone"  style="width:9rem"  aria-label="Phone">
    <button  type="submit">Add a person</button>
  </form>

  
  <h3>Work and assets</h3>
  {#if F.t(dot?.Links)}
    <ul  class="files"  data-linked>
      {#each F.rng(dot?.Links) as [k8, d8]}
        <li>
          <a  href={("/documents/" + S(d8?.ID))}>{#if F.t(d8?.Title)}{d8?.Title}{:else}Untitled{/if}</a>
          <span  class="tag">{d8?.Kind}</span>
          {#if F.t(d8?.Task)}<span  class={"status " + S(d8?.Task?.Status)}>{F.statusLabel(d8?.Task?.Status)}</span>{/if}
          <form  method="post"  action={"/companies/" + S(dot?.Company?.ID) + "/link"}  class="inline">
            <input  type="hidden"  name="target"  value={S(d8?.ID)}>
            <input  type="hidden"  name="unlink"  value="1">
            <button  type="submit"  class="ghost small">Remove</button>
          </form>
        </li>
      {/each}
    </ul>
  {:else}
    <p  class="muted small">Not attached to anything yet.</p>
  {/if}
  <form  method="post"  action={"/companies/" + S(dot?.Company?.ID) + "/link"}  class="row">
    <select  name="target"  aria-label="Attach to">
      {#each F.rng(dot?.Assignable) as [k9, d9]}
        <option  value={S(d9?.ID)}>{d9?.Kind} · {#if F.t(d9?.Title)}{d9?.Title}{:else}Untitled{/if}</option>
      {/each}
    </select>
    <button  type="submit">Attach</button>
  </form>

  <form  method="post"  action={"/companies/" + S(dot?.Company?.ID) + "/delete"}  class="row">
    <button  type="submit"  class="ghost small">Remove this company</button>
    <span  class="muted small">Its people stay, with no company, because they are
      a record of somebody who was on site.</span>
  </form>
</article>
{/snippet}
{@render content(dot)}
