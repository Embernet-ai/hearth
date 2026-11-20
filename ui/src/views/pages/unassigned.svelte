<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  import { pager } from './shared.svelte';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Unassigned</h1>
  <p  class="muted small">Records nobody owns yet. Claim one to make it yours, or hand it to the person who should have it.</p>
  <div  class="row">
    <a  class={"chip" + S((F.t(F.eq(dot?.UnownedType, "company")) ? (" on") : ""))}  href={("/crm/unassigned?type=company")}>Companies ({F.index(dot?.UnownedCounts, "company")})</a>
    <a  class={"chip" + S((F.t(F.eq(dot?.UnownedType, "contact")) ? (" on") : ""))}  href={("/crm/unassigned?type=contact")}>People ({F.index(dot?.UnownedCounts, "contact")})</a>
    <a  class={"chip" + S((F.t(F.eq(dot?.UnownedType, "deal")) ? (" on") : ""))}  href={("/crm/unassigned?type=deal")}>Deals ({F.index(dot?.UnownedCounts, "deal")})</a>
  </div>
  <form  class="row"  method="get"  action="/crm/unassigned">
    <input  type="hidden"  name="type"  value={S(dot?.UnownedType)}>
    <input  name="q"  value={S(dot?.SearchText)}  placeholder="Name"  aria-label="Search">
    <button  type="submit"  class="ghost small">Search</button>
  </form>
  {#if F.t(dot?.Unowned)}
  <table  class="list"  data-unassigned>
    <thead><tr><th>Name</th><th>Detail</th><th>Claim or hand off</th></tr></thead>
    <tbody>
      {#each F.rng(dot?.Unowned) as [k1, d1]}
      <tr  data-record={S(d1?.Ref?.ID)}>
        <td><a  href={(S(d1?.Href))}>{d1?.Name}</a></td>
        <td  class="muted small">{d1?.Subtext}</td>
        <td>
          <form  method="post"  action="/crm/owner"  class="row">
            <input  type="hidden"  name="type"  value={S(d1?.Ref?.Type)}>
            <input  type="hidden"  name="id"  value={S(d1?.Ref?.ID)}>
            <input  type="hidden"  name="back"  value={"/crm/unassigned?type=" + S(d1?.Ref?.Type)}>
            <button  type="submit"  name="owner"  value="me"  class="small">Claim</button>
          </form>
          <form  method="post"  action="/crm/owner"  class="row">
            <input  type="hidden"  name="type"  value={S(d1?.Ref?.Type)}>
            <input  type="hidden"  name="id"  value={S(d1?.Ref?.ID)}>
            <input  type="hidden"  name="back"  value={"/crm/unassigned?type=" + S(d1?.Ref?.Type)}>
            <select  name="owner"  data-autosubmit aria-label="Hand to">
              <option  value="">Hand to...</option>
              {#each F.rng(dot?.Owners) as [k2, d2]}<option  value={S(d2?.PrincipalID)}>{d2?.DisplayName}</option>{/each}
            </select>
          </form>
        </td>
      </tr>
      {/each}
    </tbody>
  </table>
  {@render pager(dot?.Pager)}
  {:else}
    <p  class="muted">Nothing is waiting here.</p>
  {/if}
</section>
{/snippet}
{@render content(dot)}
