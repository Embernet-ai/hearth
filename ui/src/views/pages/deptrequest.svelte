<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>My departments</h1>
  <p  class="muted small">
    Your departments decide who approves your hours and time off, and which
    departments you can open and file work into. You see your own work there;
    the rest of a department's work stays with the people it is shared with.
  </p>

  {#if F.t(dot?.Message)}<p  class="card small">{dot?.Message}</p>{/if}

  <div  class="card">
    {#if F.t(dot?.MyDepartments)}
      <p>You are in:
        {#each F.rng(dot?.MyDepartments) as [k1, d1]}{@const v_i = k1}{@const v_d = d1}{#if F.t(v_i)}, {/if}<strong>{v_d?.Name}</strong>{/each}</p>
    {:else}
      <p>You are not in a department yet.</p>
    {/if}

    {#if F.t(dot?.MyDeptRequest)}{@const w2 = dot?.MyDeptRequest}
      {#if F.t(F.eq(w2?.Status, "pending"))}
        <p  class="small">Waiting for confirmation: you asked for
          {#each F.rng(w2?.Asked) as [k3, d3]}{@const v_i = k3}{@const v_d = d3}{#if F.t(v_i)}, {/if}<strong>{v_d?.Name}</strong>{/each}
          on {F.date(w2?.CreatedAt)}.</p>
        <form  method="post"  action="/departments/request/cancel"  class="row"                data-confirm="Cancel your department request?">
          <button  type="submit"  class="ghost small">Cancel request</button>
        </form>
      {:else if F.t(F.eq(w2?.Status, "denied"))}
        <p  class="small"><span  class="tag over">not approved</span>
          {#if F.t(w2?.DeciderName)}by {w2?.DeciderName}{/if}{#if F.t(w2?.DecisionNote)}: {w2?.DecisionNote}{/if}</p>
      {:else if F.t(F.eq(w2?.Status, "approved"))}
        <p  class="small muted">Last request approved{#if F.t(w2?.DeciderName)} by {w2?.DeciderName}{/if}.</p>
      {/if}
    {/if}
  </div>

  <h3>{#if F.t(dot?.MyDepartments)}Ask for a change{:else}Choose your department{/if}</h3>
  {#if F.t(dot?.AllDepartments)}
    <form  method="post"  action="/departments/request"  class="stack">
      <p  class="muted small">{#if F.t(dot?.DeptApplyNow)}Tick every department you belong in, then save. It takes effect at once.{:else}Tick every department you belong in. This replaces your current set once it is approved.{/if}</p>
      {#each F.rng(dot?.AllDepartments) as [k4, d4]}
        <label  class="row"><input  type="checkbox"  name="space"  value={S(d4?.ID)} {...(F.t(F.m(dot, "DeptTicked", d4?.ID)) ? {"checked": true} : {})}> {d4?.Name}</label>
      {/each}
      {#if !F.t(dot?.DeptApplyNow)}
      <label  for="note">Anything the manager should know (optional)</label>
      <input  id="note"  name="note"  placeholder="For example: hired as a contractor for White Team">
      {/if}
      <div  class="savebar">
        <button  type="submit"  class="primary">{#if F.t(dot?.DeptApplyNow)}Save my departments{:else}Send request{/if}</button>
      </div>
    </form>
  {:else}
    <p  class="muted">There are no departments to choose from yet.</p>
  {/if}
</section>
{/snippet}
{@render content(dot)}
