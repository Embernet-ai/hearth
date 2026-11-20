<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Automations</h1>
  <p  class="muted small">
    When this happens, do that. Rules arrive switched off, because the dry run
    is the reason to trust one and it needs somewhere to happen first. Nothing
    reacts to a change another rule made, so two rules that trigger each other
    stop after one cycle.
  </p>
  {#if F.t(dot?.Message)}<p  class="err">{dot?.Message}</p>{/if}

  {#if F.t(dot?.Rules)}
    <table  class="list">
      <thead><tr><th>Rule</th><th>When</th><th>Does</th><th></th></tr></thead>
      <tbody>
        {#each F.rng(dot?.Rules) as [k1, d1]}
          <tr  data-rule={S(d1?.ID)}>
            <td>{d1?.Name}{#if F.t(F.not(d1?.Enabled))} <span  class="tag">off</span>{/if}</td>
            <td  class="muted small">
              <code>{d1?.Trigger?.Event}</code>{#if F.t(d1?.Trigger?.Kind)} on {d1?.Trigger?.Kind}{/if}
              {#each F.rng(d1?.Conditions) as [k2, d2]}<br>and {d2?.Field?.Key} {d2?.Op} {F.join(d2?.Values)}{/each}
            </td>
            <td  class="muted small">
              {#each F.rng(d1?.Actions) as [k3, d3]}<code>{d3?.Kind}</code> {d3?.Value}{d3?.Title}{d3?.Text}{d3?.URL}{/each}
            </td>
            <td  class="row">
              <form  method="post"  action={"/rules/" + S(d1?.ID) + "/dryrun"}  class="inline">
                <button  type="submit"  class="ghost small">Dry run</button>
              </form>
              <form  method="post"  action={"/rules/" + S(d1?.ID) + "/enabled"}  class="inline">
                <input  type="hidden"  name="on"  value={S((F.t(d1?.Enabled) ? ("0") : ("1")))}>
                <button  type="submit"  class="ghost small">{#if F.t(d1?.Enabled)}Turn off{:else}Turn on{/if}</button>
              </form>
              <form  method="post"  action={"/rules/" + S(d1?.ID) + "/delete"}  class="inline">
                <button  type="submit"  class="ghost small">Delete</button>
              </form>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">No rules yet.</p>
  {/if}

  <h3>New rule</h3>
  <form  method="post"  action="/rules"  class="fields">
    <label  for="name">Name</label>
    <input  id="name"  name="name"  required placeholder="Escalate urgent work orders">

    <label  for="event">When</label>
    <select  id="event"  name="event">
      <option  value="status_changed">a status changes</option>
      <option  value="assigned">somebody is assigned</option>
      <option  value="created">something is created</option>
      <option  value="updated">something is edited</option>
      <option  value="message">a comment is added</option>
    </select>

    <label  for="kind">On</label>
    <select  id="kind"  name="kind">
      <option  value="">anything</option>
      <option  value="work_order">work orders</option>
      <option  value="asset">assets</option>
      <option  value="task">tasks</option>
    </select>

    <label  for="cond_field">And if</label>
    <select  id="cond_field"  name="cond_field">
      <option  value="">no further condition</option>
      <option  value="status">status</option>
      <option  value="priority">priority</option>
      <option  value="assignee">assignee</option>
    </select>

    <label  for="cond_value">is</label>
    <input  id="cond_value"  name="cond_value"  placeholder="done">

    <label  for="action">Then</label>
    <select  id="action"  name="action">
      <option  value="comment">leave a comment</option>
      <option  value="set_field">set a field</option>
      <option  value="assign">assign it to somebody</option>
      <option  value="create_linked">raise a linked work order</option>
      <option  value="notify">notify somebody</option>
      <option  value="webhook">call a webhook</option>
    </select>

    <label  for="action_field">Field</label>
    <select  id="action_field"  name="action_field">
      <option  value="">not setting a field</option>
      <option  value="status">status</option>
      <option  value="priority">priority</option>
    </select>

    <label  for="action_value">To</label>
    <input  id="action_value"  name="action_value"  placeholder="blocked">

    <label  for="action_text">Text</label>
    <input  id="action_text"  name="action_text"  placeholder="Raised automatically.">

    <label  for="action_principal">Somebody</label>
    <select  id="action_principal"  name="action_principal">
      <option  value="">nobody</option>
      {#each F.rng(dot?.People) as [k4, d4]}<option  value={S(d4?.ID)}>{d4?.Name}</option>{/each}
    </select>

    <label  for="action_title">New item title</label>
    <input  id="action_title"  name="action_title"  placeholder="Follow-up inspection">

    <label  for="action_url">Webhook</label>
    <input  id="action_url"  name="action_url"  placeholder="https://example.invalid/hook">

    <label  for="action_secret">Signing secret</label>
    <input  id="action_secret"  name="action_secret"  type="password"  autocomplete="off">

    <div  class="row"><button  type="submit"  class="primary">Save, switched off</button></div>
  </form>

  <h3>Runs</h3>
  <p  class="muted small">
    Every consideration, including the ones that decided not to fire. "Why did
    it not fire" is the question a rules engine gets asked, and a log of only
    the firings cannot answer it.
  </p>
  {#if F.t(dot?.RuleRuns)}
    <table  class="list">
      <thead><tr><th>Rule</th><th>Outcome</th><th>Did</th><th>When</th></tr></thead>
      <tbody>
        {#each F.rng(dot?.RuleRuns) as [k5, d5]}
          <tr  data-run={S(d5?.ID)}  data-matched={S(d5?.Matched)}  data-dry={S(d5?.DryRun)}>
            <td>{d5?.RuleName}{#if F.t(d5?.DryRun)} <span  class="tag">dry run</span>{/if}</td>
            <td  class="muted small">
              {#if F.t(d5?.Matched)}matched{:else}{d5?.Reason}{/if}
              {#if F.t(d5?.Error)}<span  class="error">{d5?.Error}</span>{/if}
            </td>
            <td  class="muted small">{#each F.rng(d5?.ActionsTaken) as [k6, d6]}{d6}<br>{/each}</td>
            <td  class="muted">{F.ago(d5?.RanAt)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted small">Nothing has run yet.</p>
  {/if}
</section>
{/snippet}
{@render content(dot)}
