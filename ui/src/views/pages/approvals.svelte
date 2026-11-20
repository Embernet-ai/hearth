<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Approvals</h1>
  <p  class="muted small">
    Hours recorded by the people you manage. Sending one back does not delete
    it: the time stays, your reason goes with it, and they can correct it.
  </p>

  {#if F.t(dot?.Flash)}<p  class="card small">{dot?.Flash}</p>{/if}

  
  {#if F.t(dot?.DeptToDecide)}
    <h3  id="departments">Department requests</h3>
    {#each F.rng(dot?.DeptToDecide) as [k1, d1]}{@const v_req = d1}
      <form  method="post"  action={"/departments/requests/" + S(v_req?.ID) + "/decide"}  class="card stack">
        <input  type="hidden"  name="back"  value="/time/approvals">
        <p><strong>{v_req?.RequesterName}</strong> asked for
          {#each F.rng(v_req?.Asked) as [k2, d2]}{@const v_i = k2}{@const v_d = d2}{#if F.t(v_i)}, {/if}<strong>{v_d?.Name}</strong>{/each}
          <span  class="muted small">&middot; {F.date(v_req?.CreatedAt)}
          &middot; now in {#each F.rng(v_req?.Current) as [k3, d3]}{@const v_i = k3}{@const v_d = d3}{#if F.t(v_i)}, {/if}{v_d?.Name}{:else}no department{/each}</span></p>
        {#if F.t(v_req?.Note)}<p  class="muted small">&ldquo;{v_req?.Note}&rdquo;</p>{/if}
        <div  class="row">
          {#each F.rng(dot?.AllDepartments) as [k4, d4]}
            <label  style="white-space:nowrap"><input  type="checkbox"  name="space"  value={S(d4?.ID)}  data-label={S(d4?.Name)} {...(F.t(F.m(v_req, "AskedHas", d4?.ID)) ? {"checked": true} : {})}> {d4?.Name}</label>
          {/each}
        </div>
        <div  class="row">
          <input  name="note"  placeholder="Reason, if saying no"  aria-label="Reason"  style="width:16rem">
          <button  type="submit"  name="decision"  value="approve"  data-confirm-list="space"                    data-confirm={"Are you sure " + S(v_req?.RequesterName) + " should be in these departments? They will be able to open them and add work to them, and see their own work there."}>Approve</button>
          <button  type="submit"  name="decision"  value="deny"  class="ghost small">Say no</button>
        </div>
      </form>
    {/each}
  {/if}

  {#if F.t(dot?.Approvals)}{@const v_v = dot?.Approval}
    
    
    <div  class="card">
      <p><strong>{v_v?.Entries}</strong> entries waiting &middot; <strong>{F.hours(v_v?.Minutes)}</strong>
        &middot; {v_v?.Waiting} {#if F.t(F.eq(v_v?.Waiting, 1))}person{:else}people{/if}</p>

      
      <form  method="get"  action="/time/approvals"  class="row">
        <label  for="dept">Department</label>
        <select  id="dept"  name="dept"  data-autosubmit>
          <option  value="">All departments</option>
          {#each F.rng(v_v?.Depts) as [k5, d5]}
            <option  value={S(d5?.Value)} {...(F.t(F.eq(d5?.Value, v_v?.Dept)) ? {"selected": true} : {})}>{d5?.Label} ({d5?.Entries} &middot; {F.hours(d5?.Minutes)})</option>
          {/each}
        </select>
        <label  for="person">Employee</label>
        <select  id="person"  name="person"  data-autosubmit>
          <option  value="">Choose somebody</option>
          {#each F.rng(v_v?.People) as [k6, d6]}
            <option  value={S(d6?.PersonID)} {...(F.t(F.eq(d6?.PersonID, v_v?.Person)) ? {"selected": true} : {})}>{d6?.PersonName} ({F.len(d6?.Entries)} &middot; {F.hours(d6?.Minutes)})</option>
          {/each}
        </select>
        <button  type="submit"  class="small ghost">Show</button>
      </form>
    </div>

    {#if F.t(v_v?.Selected)}{@const w7 = v_v?.Selected}
      
      <h3>{w7?.PersonName}
        <span  class="muted small">{F.len(w7?.Entries)} entries &middot; {F.hours(w7?.Minutes)}
          {#each F.rng(w7?.Departments) as [k8, d8]}{@const v_i = k8}{@const v_d = d8}{#if F.t(v_i)}, {:else} &middot; {/if}{v_d?.Name}{/each}</span></h3>

      <div  class="row">
        
        <form  id="bulk"  method="post"  action="/time/approvals/bulk"  class="row"                data-confirm="Approve every entry you ticked?">
          <input  type="hidden"  name="back"  value={S(v_v?.Back)}>
          <button  type="submit"  class="small">Approve selected</button>
        </form>
        
        <form  method="post"  action="/time/approvals/bulk"  class="row"                data-confirm={"Approve all " + S(F.len(w7?.Entries)) + " entries (" + S(F.hours(w7?.Minutes)) + ") for " + S(w7?.PersonName) + "?"}>
          <input  type="hidden"  name="back"  value={S(v_v?.Back)}>
          {#each F.rng(w7?.Entries) as [k9, d9]}<input  type="hidden"  name="entry"  value={S(d9?.ID)}>{/each}
          <button  type="submit"  class="small">Approve all {F.len(w7?.Entries)}</button>
        </form>
      </div>

      <table  class="list">
        <thead>
          <tr><th></th><th>Day</th><th>How long</th><th>Against</th><th>Note</th><th></th></tr>
        </thead>
        <tbody>
          {#each F.rng(w7?.Entries) as [k10, d10]}
            <tr>
              <td><input  type="checkbox"  name="entry"  value={S(d10?.ID)}  {@attach A("form", "bulk")}  aria-label="Select"></td>
              <td>{F.date(d10?.StartedAt)}</td>
              <td>{F.hours(d10?.Elapsed)}</td>
              <td>
                {#if F.t(d10?.Unattached)}
                  <span  class="muted">&mdash;</span>
                {:else}
                  <a  href={("/documents/" + S(d10?.DocumentID))}>{#if F.t(d10?.DocTitle)}{d10?.DocTitle}{:else}a job{/if}</a>
                {/if}
              </td>
              <td  class="muted">{d10?.Note}</td>
              <td>
                
                <form  method="post"  action={"/time/approvals/" + S(d10?.ID)}  class="row">
                  <input  type="hidden"  name="back"  value={S(v_v?.Back)}>
                  <input  name="note"  placeholder="Reason, if sending back"                           aria-label="Reason"  style="width:14rem">
                  <button  type="submit"  name="decision"  value="approve">Approve</button>
                  <button  type="submit"  name="decision"  value="back"  class="ghost small">Send back</button>
                </form>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      
      <table  class="list">
        <thead>
          <tr><th>Employee</th><th>Departments</th><th>Entries</th><th>Hours</th><th></th></tr>
        </thead>
        <tbody>
          {#each F.rng(v_v?.People) as [k11, d11]}
            <tr>
              <td>{d11?.PersonName}</td>
              <td  class="muted small">{#each F.rng(d11?.Departments) as [k12, d12]}{@const v_i = k12}{@const v_d = d12}{#if F.t(v_i)}, {/if}{v_d?.Name}{:else}No department{/each}</td>
              <td>{F.len(d11?.Entries)}</td>
              <td>{F.hours(d11?.Minutes)}</td>
              <td><a  class="chip"  href={("/time/approvals?" + S((F.t(v_v?.Dept) ? ("dept=" + S(v_v?.Dept) + "&") : "")) + "person=" + S(d11?.PersonID))}>Open</a></td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  {:else if F.t(F.not(dot?.DeptToDecide))}
    <p  class="muted">
      Nothing is waiting on you. If you expected to see somebody here, they
      either have no department set or nobody manages theirs.
    </p>
  {/if}

  
  <h3>Already decided
    <span  class="muted small">
      {#if F.t(dot?.SeeAllTime)}everyone's, last three months{:else}what you decided, last three months{/if}
    </span></h3>
  {#if F.t(dot?.DecidedTime)}
    <table  class="list">
      <thead>
        <tr><th>Person</th><th>Day</th><th>How long</th><th>Against</th><th>Decision</th></tr>
      </thead>
      <tbody>
        {#each F.rng(dot?.DecidedTime) as [k13, d13]}
          <tr>
            <td>{d13?.PersonName}</td>
            <td>{F.date(d13?.StartedAt)}</td>
            <td>{F.hours(d13?.Elapsed)}</td>
            <td>
              {#if F.t(d13?.Unattached)}
                <span  class="muted">&mdash;</span>
              {:else}
                <a  href={("/documents/" + S(d13?.DocumentID))}>{#if F.t(d13?.DocTitle)}{d13?.DocTitle}{:else}a job{/if}</a>
              {/if}
            </td>
            <td>
              {#if F.t(d13?.WasApproved)}
                <span  class="tag">approved{#if F.t(d13?.DeciderName)} by {d13?.DeciderName}{/if}</span>
              {:else}
                <span  class="tag over">sent back{#if F.t(d13?.DeciderName)} by {d13?.DeciderName}{/if}</span>
                {#if F.t(d13?.DecidedNote)}<div  class="muted small">{d13?.DecidedNote}</div>{/if}
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">Nothing decided yet.</p>
  {/if}
</section>
{/snippet}
{@render content(dot)}
