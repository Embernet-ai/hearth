<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section  class="card timeoff">
  <h1>Time off</h1>
  {#if F.t(dot?.Message)}<p  class="ok">{dot?.Message}</p>{/if}

  {#if F.t(dot?.TimeOffToDecide)}
    
    <h2  id="to-decide">To decide</h2>
    <ul  class="plain timeoff-list">
      {#each F.rng(dot?.TimeOffToDecide) as [k1, d1]}
        <li  id={"r-" + S(d1?.ID)}  class="timeoff-item">
          <div  class="row spread">
            <strong>{d1?.RequesterName}</strong>
            <span  class={"status timeoff-" + S(d1?.Status)}>{d1?.Status}</span>
          </div>
          <div>{F.leaveKind(d1?.Kind)} &middot; {F.m(d1?.StartsOn, "Format", "Mon 2 Jan")} – {F.m(d1?.EndsOn, "Format", "Mon 2 Jan 2006")} &middot; <strong>{d1?.Days}</strong> working day{#if F.t(F.ne(d1?.Days, 1))}s{/if}</div>
          {#if F.t(d1?.Reason)}<p  class="muted">Reason: {d1?.Reason}</p>{/if}
          {#if F.t(d1?.Comments)}<p  class="muted">Comments: {d1?.Comments}</p>{/if}
          {#if F.t(F.eq(d1?.Status, "pending"))}
            <form  method="post"  action={"/time-off/" + S(d1?.ID) + "/decide"}  class="stack">
              <input  name="note"  placeholder="Comment (optional)">
              <div  class="row">
                <button  type="submit"  name="approve"  value="1"  class="primary">Approve</button>
                <button  type="submit"  name="approve"  value="0"  class="ghost">Deny</button>
              </div>
            </form>
          {:else}
            <p  class="muted small">{d1?.Status} by {d1?.DeciderName}{#if F.t(d1?.DecidedAt)} {F.ago(d1?.DecidedAt)}{/if}{#if F.t(d1?.DecisionNote)}: {d1?.DecisionNote}{/if}</p>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}

  <h2>Request time off</h2>
  <p  class="muted small">Your request goes to your approver. Only you, whoever approves it, and HR can see it. Your team only sees "Out" on the calendar once it's approved, never the reason.</p>
  <form  method="post"  action="/time-off"  class="stack timeoff-form">
    <div  class="row">
      <label  class="stack">From <input  type="date"  name="starts_on"  required></label>
      <label  class="stack">To <input  type="date"  name="ends_on"  required></label>
    </div>
    <label  class="stack">Type of leave
      <select  name="kind"  required>
        <option  value="">Choose…</option>
        {#each F.rng(dot?.TimeOffKinds) as [k2, d2]}<option  value={S(d2?.Key)}>{d2?.Label}</option>{/each}
      </select>
    </label>
    <label  class="stack">Reason <textarea  name="reason"  rows="2"  placeholder="Optional"></textarea></label>
    <label  class="stack">Other comments <textarea  name="comments"  rows="2"  placeholder="Optional"></textarea></label>
    <label  class="row"><input  type="checkbox"  name="acknowledged"  required> I understand that this request is subject to approval.</label>
    <div  class="row"><button  type="submit"  class="primary">Send request</button></div>
    <p  class="muted small">Working days (Monday to Friday) are counted for you.</p>
  </form>

  <h2>My requests</h2>
  {#if F.t(dot?.TimeOff)}
    <ul  class="plain timeoff-list">
      {#each F.rng(dot?.TimeOff) as [k3, d3]}
        <li  id={"r-" + S(d3?.ID)}  class="timeoff-item">
          <div  class="row spread">
            <span>{F.leaveKind(d3?.Kind)} &middot; {F.m(d3?.StartsOn, "Format", "Mon 2 Jan")} – {F.m(d3?.EndsOn, "Format", "Mon 2 Jan 2006")} &middot; {d3?.Days} day{#if F.t(F.ne(d3?.Days, 1))}s{/if}</span>
            <span  class={"status timeoff-" + S(d3?.Status)}>{d3?.Status}</span>
          </div>
          {#if F.t(d3?.DeciderName)}<p  class="muted small">by {d3?.DeciderName}{#if F.t(d3?.DecisionNote)}: {d3?.DecisionNote}{/if}</p>{/if}
          {#if F.t(F.or(F.eq(d3?.Status, "pending"), F.eq(d3?.Status, "approved")))}
            <form  method="post"  action={"/time-off/" + S(d3?.ID) + "/cancel"}  class="inline"                    data-confirm="Cancel this time off request?">
              <button  type="submit"  class="ghost small">Cancel request</button>
            </form>
          {/if}
        </li>
      {/each}
    </ul>
  {:else}
    <p  class="muted small">No requests yet.</p>
  {/if}
</section>
{/snippet}
{@render content(dot)}
