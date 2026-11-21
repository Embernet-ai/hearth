<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}{@const v_p = dot?.Person}{@const v_id = v_p?.PrincipalID}{@const v_back = F.printf("/people/%s", v_id)}{@const v_st = F.index(dot?.Chart, v_id)}{@const v_sees = F.index(dot?.SpaceGrants, v_id)}{@const v_held = F.index(dot?.Grants, v_id)}






<section>
  <p  class="small"><a  href={("/people")}>&larr; All people</a></p>
  <h1>{v_p?.DisplayName}
    {#if F.t(v_p?.IsOwner)}<span  class="tag">owner</span>{/if}
    {#if F.t(F.ne(v_p?.Status, "active"))}<span  class="tag over">{v_p?.Status}</span>{/if}
  </h1>
  <p  class="muted small">
    {#if F.t(v_p?.Email)}{v_p?.Email}{:else}Signs in with single sign-on{/if} &middot;
    last seen {#if F.t(v_p?.LastLoginAt)}{F.ago(v_p?.LastLoginAt)}{:else}never{/if}
  </p>

  {#if F.t(dot?.CanMovePeople)}{#if F.t(dot?.AskDept)}{@const w1 = dot?.AskDept}{@const v_dept = w1}
    
    <div  class="card askaccess">
      <h3>Can {v_p?.DisplayName} see {v_dept?.Name}'s work?</h3>
      <p  class="muted small">
        You just added them to {v_dept?.Name}. That puts them on the org chart
        but doesn't let them see anything there yet.
      </p>
      <div  class="row"  style="flex-wrap:wrap">
        <form  method="post"  action={"/people/" + S(v_id) + "/grant-space"}  class="inline">
          <input  type="hidden"  name="back"  value={S(v_back)}>
          <input  type="hidden"  name="space"  value={S(v_dept?.ID)}>
          <button  type="submit"  class="primary">Yes, all of {v_dept?.Name}</button>
        </form>
        <a  href={(S(v_back))}  class="small">Not now</a>
      </div>
      {#if F.t(dot?.AskParts)}
        <details>
          <summary>Only some parts</summary>
          <form  method="post"  action={"/people/" + S(v_id) + "/grant-space"}  class="stack">
            <input  type="hidden"  name="back"  value={S(v_back)}>
            {#each F.rng(dot?.AskParts) as [k2, d2]}
              <label  class={"askpart indent" + S(d2?.Indent)}>
                <input  type="checkbox"  name="space"  value={S(d2?.Space?.ID)}> {d2?.Space?.Name}
              </label>
            {/each}
            <div  class="row"><button  type="submit">Give access to what I ticked</button></div>
          </form>
        </details>
      {/if}
    </div>
  {/if}{/if}

  <div  class="personsections">

  <div  class="card">
    <h3>Works in</h3>
    <p  class="muted small">Decides who approves their hours, and lets them open the department and add work to it. They see their own work there; seeing everyone's is &ldquo;Can see work in&rdquo; below.</p>
    
    <div  class="row"  style="flex-wrap:wrap">
      {#if F.t(F.and(v_st, v_st?.Departments))}
        {#each F.rng(v_st?.Departments) as [k3, d3]}{@const v_d = d3}
          {#if F.t(dot?.CanMovePeople)}
          <form  method="post"  action={"/people/" + S(v_id) + "/departments"}  class="inline">
            <input  type="hidden"  name="back"  value={S(v_back)}>
            {#each F.rng(v_st?.Departments) as [k4, d4]}{@const v_o = d4}{#if F.t(F.ne(v_o?.ID, v_d?.ID))}<input  type="hidden"  name="department"  value={S(v_o?.ID)}>{/if}{/each}
            <button  type="submit"  class="chip"  title={"Take them out of " + S(v_d?.Name)}>{v_d?.Name} &times;</button>
          </form>
          {:else}<span  class="tag">{v_d?.Name}</span>{/if}
        {/each}
      {:else}
        <span  class="muted small">Not in any department yet.</span>
      {/if}
    </div>
    {#if F.t(dot?.CanMovePeople)}
    <form  method="post"  action={"/people/" + S(v_id) + "/departments"}  class="row">
      <input  type="hidden"  name="back"  value={S(v_back)}>
      <input  type="hidden"  name="ask"  value="1">
      {#if F.t(v_st)}{#each F.rng(v_st?.Departments) as [k5, d5]}<input  type="hidden"  name="department"  value={S(d5?.ID)}>{/each}{/if}
      <select  name="department"  required aria-label="Add a department">
        <option  value="">Add a department&hellip;</option>
        {#each F.rng(dot?.Departments) as [k6, d6]}{#if F.t(F.not(F.m(v_st, "In", d6?.ID)))}<option  value={S(d6?.ID)}>{#if F.t(F.eq(d6?.Depth, 1))}&nbsp;&nbsp;{/if}{d6?.Name}</option>{/if}{/each}
      </select>
      <button  type="submit"  class="ghost small">Add</button>
    </form>
    {/if}
  </div>

  <div  class="card">
    <h3>Hours approved by</h3>
    <p  class="muted small">Who signs off their time. &ldquo;Their department&rdquo; means whoever manages a department they work in.</p>
    {#if F.t(F.not(dot?.CanMovePeople))}
      <p>{#if F.t(F.and(v_st, v_st?.ApproverName))}{v_st?.ApproverName}{:else}Their department{/if}</p>
    {:else}
    <form  method="post"  action={"/people/" + S(v_id) + "/approver"}  class="row">
      <input  type="hidden"  name="back"  value={S(v_back)}>
      <select  name="approver"  data-autosubmit aria-label="Hours approved by">
        <option  value="">Their department</option>
        {#each F.rng(dot?.Persons) as [k7, d7]}{@const v_o = d7}{#if F.t(F.ne(v_o?.PrincipalID, v_id))}
          <option  value={S(v_o?.PrincipalID)} {...(F.t(F.and(v_st, F.eq(v_st?.ApproverID, v_o?.PrincipalID))) ? {"selected": true} : {})}>{v_o?.DisplayName}</option>
        {/if}{/each}
      </select>
      <noscript><button  type="submit"  class="ghost small">Set</button></noscript>
    </form>
    {/if}
  </div>

  <div  class="card">
    <h3>Time approval</h3>
    <p  class="muted small">For salaried staff. Time they log from the moment this is turned on is approved as it is logged, and the entry says the system approved it. Time already logged, or dated before then, keeps the state it has.</p>
    {#if F.t(F.not(dot?.CanMovePeople))}
      <p>{#if F.t(F.and(v_st, v_st?.TimeAutoApprove))}Approved automatically{:else}Each entry waits for an approver{/if}</p>
    {:else}
    <form  method="post"  action={"/people/" + S(v_id) + "/time-auto-approve"}  class="row">
      <input  type="hidden"  name="back"  value={S(v_back)}>
      <select  name="auto"  data-autosubmit aria-label="Time approval">
        <option  value="">Each entry waits for an approver</option>
        <option  value="on" {...(F.t(F.and(v_st, v_st?.TimeAutoApprove)) ? {"selected": true} : {})}>Approved automatically (salaried)</option>
      </select>
      <noscript><button  type="submit"  class="ghost small">Set</button></noscript>
    </form>
    {/if}
  </div>

  <div  class="card">
    <h3>Manages</h3>
    <p  class="muted small">Departments whose people's hours this person approves.</p>
    <div  class="row"  style="flex-wrap:wrap">
      {#if F.t(dot?.PersonManaged)}
        {#each F.rng(dot?.PersonManaged) as [k8, d8]}
          {#if F.t(dot?.CanMovePeople)}
          <form  method="post"  action={"/people/" + S(v_id) + "/manages"}  class="inline">
            <input  type="hidden"  name="back"  value={S(v_back)}>
            <input  type="hidden"  name="department"  value={S(d8?.ID)}>
            <button  type="submit"  name="action"  value="remove"  class="chip"  title={"Stop managing " + S(d8?.Name)}>{d8?.Name} &times;</button>
          </form>
          {:else}<span  class="tag">{d8?.Name}</span>{/if}
        {/each}
      {:else}
        <span  class="muted small">Doesn't manage any department.</span>
      {/if}
    </div>
    {#if F.t(dot?.CanMovePeople)}
    <form  method="post"  action={"/people/" + S(v_id) + "/manages"}  class="row">
      <input  type="hidden"  name="back"  value={S(v_back)}>
      <select  name="department"  required aria-label="Manage a department">
        <option  value="">Add a department&hellip;</option>
        {#each F.rng(dot?.Departments) as [k9, d9]}<option  value={S(d9?.ID)}>{#if F.t(F.eq(d9?.Depth, 1))}&nbsp;&nbsp;{/if}{d9?.Name}</option>{/each}
      </select>
      <button  type="submit"  name="action"  value="add"  class="ghost small">Add</button>
    </form>
    {/if}
  </div>

  <div  class="card wide">
    <h3>Can see work in</h3>
    <p  class="muted small">
      Tick what they can open. Ticking a department includes everything in it.
      To give only some teams, untick the department, save, then tick the teams.
    </p>
    {#if F.t(F.not(v_sees))}
      <p><span  class="tag over">Nothing yet</span>
        <span  class="muted small">They can sign in but won't see any department's work.</span></p>
    {/if}
    {#if F.t(dot?.CanMovePeople)}<form  method="post"  action={"/people/" + S(v_id) + "/access"}  class="accesstree"  data-access-tree>
      <input  type="hidden"  name="back"  value={S(v_back)}>
      {#each F.rng(dot?.AccessGroups) as [k10, d10]}
        <details {...(F.t(d10?.Open) ? {"open": true} : {})}>
          <summary>
            <label  class="accessnode">
              <input  type="checkbox"  name="space"  value={S(d10?.Dept?.Space?.ID)}  data-depth="0" {...(F.t(d10?.Dept?.Checked) ? {"checked": true} : {})} {...(F.t(F.not(dot?.CanMovePeople)) ? {"disabled": true} : {})}>
              <strong>{d10?.Dept?.Space?.Name}</strong>
            </label>
          </summary>
          {#each F.rng(d10?.Parts) as [k11, d11]}
            <label  class={"accessnode indent" + S(d11?.Indent) + S((F.t(d11?.Covered) ? (" covered") : ""))}>
              <input  type="checkbox"  name="space"  value={S(d11?.Space?.ID)}  data-depth={S(d11?.Indent)} {...(F.t(F.or(d11?.Checked, d11?.Covered)) ? {"checked": true} : {})} {...(F.t(F.or(d11?.Covered, F.not(dot?.CanMovePeople))) ? {"disabled": true} : {})}>
              {d11?.Space?.Name} <span  class="muted small incl" {...(F.t(F.not(d11?.Covered)) ? {"hidden": true} : {})}>included</span>
            </label>
          {/each}
        </details>
      {/each}
      {#if F.t(dot?.CanMovePeople)}
        <div  class="row"><button  type="submit"  class="primary">Save access</button></div>
      {:else}
        <p  class="muted small">Only an admin can change who sees what.</p>
      {/if}
    </form>{:else}<div  class="accesstree">
      <input  type="hidden"  name="back"  value={S(v_back)}>
      {#each F.rng(dot?.AccessGroups) as [k12, d12]}
        <details {...(F.t(d12?.Open) ? {"open": true} : {})}>
          <summary>
            <label  class="accessnode">
              <input  type="checkbox"  name="space"  value={S(d12?.Dept?.Space?.ID)}  data-depth="0" {...(F.t(d12?.Dept?.Checked) ? {"checked": true} : {})} {...(F.t(F.not(dot?.CanMovePeople)) ? {"disabled": true} : {})}>
              <strong>{d12?.Dept?.Space?.Name}</strong>
            </label>
          </summary>
          {#each F.rng(d12?.Parts) as [k13, d13]}
            <label  class={"accessnode indent" + S(d13?.Indent) + S((F.t(d13?.Covered) ? (" covered") : ""))}>
              <input  type="checkbox"  name="space"  value={S(d13?.Space?.ID)}  data-depth={S(d13?.Indent)} {...(F.t(F.or(d13?.Checked, d13?.Covered)) ? {"checked": true} : {})} {...(F.t(F.or(d13?.Covered, F.not(dot?.CanMovePeople))) ? {"disabled": true} : {})}>
              {d13?.Space?.Name} <span  class="muted small incl" {...(F.t(F.not(d13?.Covered)) ? {"hidden": true} : {})}>included</span>
            </label>
          {/each}
        </details>
      {/each}
      {#if F.t(dot?.CanMovePeople)}
        <div  class="row"><button  type="submit"  class="primary">Save access</button></div>
      {:else}
        <p  class="muted small">Only an admin can change who sees what.</p>
      {/if}
    </div>{/if}
  </div>

  <div  class="card">
    <h3>Can open</h3>
    <p  class="muted small">Admin pages. Separate from &ldquo;Can see work in&rdquo;: Finance here is the billing report, not the Finance department.</p>
    {#if F.t(F.and(v_held, v_held?.IsSuperAdmin))}
      <p><span  class="tag">super admin</span> <span  class="muted small">Everything, and can hand out the rest.</span></p>
    {/if}
    {#if F.t(dot?.CanGrant)}
      <table  class="list">
        <tbody>
          {#each F.rng(dot?.Grantable) as [k14, d14]}{@const v_c = d14}
            <tr>
              <td><strong>{v_c?.Label}</strong><div  class="muted small">{v_c?.Describes}</div></td>
              <td>
                {#if F.t(F.and(v_held, v_held?.IsSuperAdmin))}
                  <span  class="muted small">included</span>
                {:else}
                  <form  method="post"  action={"/people/" + S(v_id) + "/capability"}  class="inline">
                    <input  type="hidden"  name="back"  value={S(v_back)}>
                    <input  type="hidden"  name="capability"  value={S(v_c)}>
                    {#if F.t(F.and(v_held, F.index(v_held, v_c)))}
                      <input  type="hidden"  name="action"  value="revoke">
                      <span  class="tag">Yes</span> <button  type="submit"  class="ghost small">Remove</button>
                    {:else}
                      <input  type="hidden"  name="action"  value="grant">
                      <span  class="muted small">No</span> <button  type="submit"  class="small">Give</button>
                    {/if}
                  </form>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
      
      <form  method="post"  action={"/people/" + S(v_id) + "/capability"}  class="row"              data-confirm={S((F.t(F.and(v_held, v_held?.IsSuperAdmin)) ? ("Stand " + S(v_p?.DisplayName) + " down from super admin?") : ("Make " + S(v_p?.DisplayName) + " a super admin? They get everything.")))}>
        <input  type="hidden"  name="back"  value={S(v_back)}>
        <input  type="hidden"  name="capability"  value="super_admin">
        {#if F.t(F.and(v_held, v_held?.IsSuperAdmin))}
          <input  type="hidden"  name="action"  value="revoke">
          <button  type="submit"  class="ghost small">Stand down from super admin</button>
        {:else}
          <input  type="hidden"  name="action"  value="grant">
          <button  type="submit"  class="ghost small">Make super admin</button>
        {/if}
      </form>
    {:else}
      <p>{#each F.rng(dot?.Grantable) as [k15, d15]}{#if F.t(F.and(v_held, F.index(v_held, d15)))}<span  class="tag">{d15?.Label}</span> {/if}{/each}</p>
      <p  class="muted small">Only a super admin can change these.</p>
    {/if}
  </div>

  
  {#if F.t(F.and(dot?.IsAdmin, F.not(v_p?.IsOwner)))}
    <div  class="card">
      <h3>Account</h3>
      
      <form  method="post"  action={"/people/" + S(v_id) + "/status"}  class="row"              data-confirm={S((F.t(F.eq(v_p?.Status, "active")) ? ("Suspend " + S(v_p?.DisplayName) + "? They can't sign in until reinstated.") : ("Reinstate " + S(v_p?.DisplayName) + "?")))}>
        <input  type="hidden"  name="back"  value={S(v_back)}>
        {#if F.t(F.eq(v_p?.Status, "active"))}
          <input  type="hidden"  name="status"  value="suspended">
          <button  type="submit"  class="ghost small">Suspend</button>
          <span  class="muted small">They can't sign in until reinstated. Nothing they did is deleted.</span>
        {:else}
          <input  type="hidden"  name="status"  value="active">
          <button  type="submit"  class="small">Reinstate</button>
        {/if}
      </form>
      <form  method="post"  action={"/people/" + S(v_id) + "/password-link"}  class="row"              data-confirm={"Make a new set-password link for " + S(v_p?.DisplayName) + "? Any link they already have stops working."}>
        <input  type="hidden"  name="back"  value={S(v_back)}>
        <button  type="submit"  class="ghost small">Reset password</button>
        <span  class="muted small">Makes a link they open to choose a new password. It works once and expires in 24 hours.</span>
      </form>
      <form  method="post"  action={"/people/" + S(v_id) + "/contractor"}  class="row">
        <input  type="hidden"  name="back"  value={S(v_back)}>
        <label  class="small"><input  type="checkbox"  name="contractor" {...(F.t(v_p?.Contractor) ? {"checked": true} : {})}> Contractor account</label>
        <button  type="submit"  class="ghost small">Save</button>
        <span  class="muted small">A contractor account reaches only the folders a manager gives it. It is in no department and does not see the daily reports department.</span>
      </form>
    </div>
  {/if}

  </div>
</section>
{/snippet}
{@render content(dot)}
