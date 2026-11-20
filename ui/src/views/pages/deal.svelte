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
    <h1>{dot?.Deal?.Name}</h1>
    <div  class="row">
      <span  class="tag">{F.stageName(dot?.Deal?.Stage)}</span>
      {@render favstar(dot, "deal", dot?.Deal?.ID)}
      <a  class="chip"  href={("/deals")}>All deals</a>
    </div>
  </div>
  {#if F.t(dot?.EmberOps)}
  <p  class="row wrap small"  data-emberops>In EmberOps: {#each F.rng(dot?.EmberOps) as [ke, de]}<a  class="chip"  href={S(de?.URL)}  target="_blank"  rel="noopener">{de?.Label}</a>{/each}</p>
  {/if}
  <form  class="fields"  method="post"  action={"/deals/" + S(dot?.Deal?.ID)}  data-deal-form>
    <label  for="name">Name</label>
    <input  id="name"  name="name"  value={S(dot?.Deal?.Name)}  required>
    <label  for="stage">Stage</label>
    <select  id="stage"  name="stage">
      {#each F.rng(dot?.DealStages) as [k1, d1]}<option  value={S(d1)}  {...(F.t(F.eq(d1, dot?.Deal?.Stage)) ? {"selected": true} : {})}>{F.stageName(d1)}</option>{/each}
    </select>
    <label  for="amount">Amount</label>
    <input  id="amount"  name="amount"  value={S(F.dollars(F.i64p(dot?.Deal?.AmountCents)))}  placeholder="25000">
    <label  for="close">Close date</label>
    <input  id="close"  name="close"  type="date"  value={S(F.dateonly(dot?.Deal?.CloseDate))}>
    <label  for="company">Company</label>
    <select  id="company"  name="company">
      <option  value="">No company</option>
      {#each F.rng(dot?.Companies) as [k2, d2]}<option  value={S(d2?.ID)}  {...(F.t(F.same(d2?.ID, dot?.Deal?.CompanyID)) ? {"selected": true} : {})}>{d2?.Name}</option>{/each}
      {#if F.t(dot?.Deal?.CompanyID)}{#if F.t(F.not(dot?.Companies))}<option  value={S(F.deref_s(dot?.Deal?.CompanyID))}  selected>{dot?.Deal?.CompanyName}</option>{/if}{/if}
    </select>
    <label  for="contact">Point of contact</label>
    <select  id="contact"  name="contact">
      <option  value="">Nobody</option>
      {#each F.rng(dot?.Contacts) as [k3, d3]}<option  value={S(d3?.ID)}  {...(F.t(F.same(d3?.ID, dot?.Deal?.ContactID)) ? {"selected": true} : {})}>{d3?.Name}</option>{/each}
      {#if F.t(dot?.Deal?.ContactID)}<option  value={S(F.deref_s(dot?.Deal?.ContactID))}  selected>{dot?.Deal?.ContactName}</option>{/if}
    </select>
    <label>Owner</label>
    {@render ownerselect(F.dict("Name", "owner", "People", dot?.Owners, "Selected", F.deref_s(dot?.Deal?.OwnerID)))}
    <label  for="notes">Notes</label>
    <textarea  id="notes"  name="notes"  rows="3">{dot?.Deal?.Notes}</textarea>
    <div  class="row"><button  type="submit"  class="primary">Save</button></div>
  </form>

  {@render timeline(dot)}
  {@render addactivity(F.dict("Type", "deal", "ID", dot?.Deal?.ID))}

  <form  method="post"  action={"/deals/" + S(dot?.Deal?.ID) + "/delete"}  class="row">
    <button  type="submit"  class="ghost small"  data-confirm="Remove this deal?">Remove this deal</button>
  </form>
</article>
{/snippet}
{@render content(dot)}
