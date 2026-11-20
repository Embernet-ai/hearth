<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Agents</h1>
  <p  class="muted small">
    An agent is a principal, so it has grants, appears in history, and can be
    mentioned. It sees exactly what it has been granted and nothing else,
    because the permission question it hits is the one your own requests hit.
  </p>

  {#if F.t(dot?.NewToken)}
    <div  class="card"  data-new-token>
      <h3>Here is the token</h3>
      <p  class="muted small">
        Once. There is nowhere to read it back from, which is the point: a
        token you can recover from a page is a token that leaks through a
        screen share. Lost one gets revoked and replaced.
      </p>
      <pre  class="schema">{dot?.NewToken}</pre>
      <p  class="muted small">
        Point a client at <code>{dot?.MCPURL}</code> with
        <code>Authorization: Bearer &lt;token&gt;</code>.
      </p>
    </div>
  {/if}

  {#if F.t(dot?.Suggestions)}
    <h2>Waiting for you</h2>
    <p  class="muted small">
      Nothing here has changed anything. An agent that writes directly is one
      bad prompt away from reassigning every work order on site, and being
      able to see who did it afterwards does not help the shift that lost its
      morning.
    </p>
    <ul  class="files"  data-suggestions>
      {#each F.rng(dot?.Suggestions) as [k1, d1]}
        <li  data-suggestion={S(d1?.ID)}>
          <strong>{d1?.Summary}</strong>
          <span  class="tag">{d1?.AgentName}</span>
          {#if F.t(d1?.Reason)}<span  class="muted small">{d1?.Reason}</span>{/if}
          <span  class="muted small">{F.ago(d1?.CreatedAt)}</span>
          {#if F.t(d1?.EntityID)}
            <a  href={("/documents/" + S(F.deref_s(d1?.EntityID)))}>{d1?.EntityTitle}</a>
          {/if}
          <form  method="post"  action={"/suggestions/" + S(d1?.ID)}  class="inline">
            <input  type="hidden"  name="decision"  value="accept">
            <button  type="submit"  class="ghost small">Accept</button>
          </form>
          <form  method="post"  action={"/suggestions/" + S(d1?.ID)}  class="inline">
            <input  type="hidden"  name="decision"  value="reject">
            <button  type="submit"  class="ghost small">Reject</button>
          </form>
        </li>
      {/each}
    </ul>
  {/if}

  <h2>The agents</h2>
  {#if F.t(dot?.Agents)}
    <table  class="list">
      <thead><tr><th>Agent</th><th>Handle</th><th>Channels</th><th>Tokens</th><th>Waiting</th><th></th></tr></thead>
      <tbody>
        {#each F.rng(dot?.Agents) as [k2, d2]}
          <tr  data-agent={S(d2?.ID)}>
            <td>{d2?.Name}{#if F.t(F.not(d2?.Active))} <span  class="tag">suspended</span>{/if}</td>
            <td  class="muted small">@{d2?.Handle}</td>
            <td  class={S((F.t(d2?.Isolated) ? ("over") : ""))}  data-channels={S(d2?.Channels)}>
              {d2?.Channels}
              {#if F.t(d2?.Isolated)}<span  class="muted small">nothing it makes will
                be visible to anybody else</span>{/if}
            </td>
            <td  class="muted"  data-tokens={S(d2?.Tokens)}>{d2?.Tokens}</td>
            <td  class="muted">{d2?.Suggestions}</td>
            <td>
              <form  method="post"  action={"/agents/" + S(d2?.ID) + "/status"}  class="inline">
                <input  type="hidden"  name="status"                         value={S((F.t(d2?.Active) ? ("suspended") : ("active")))}>
                <button  type="submit"  class="ghost small">
                  {#if F.t(d2?.Active)}Suspend{:else}Restore{/if}</button>
              </form>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">No agents yet.</p>
  {/if}

  <form  method="post"  action="/agents"  class="row wrap">
    <input  name="name"  placeholder="Shift assistant"  required aria-label="Name">
    <input  name="handle"  placeholder="handle"  style="width:9rem"  aria-label="Handle">
    <select  name="channel"  aria-label="Channel">
      <option  value="">No channel</option>
      {#each F.rng(dot?.Channels) as [k3, d3]}<option  value={S(d3?.ID)}>{d3?.Name}</option>{/each}
    </select>
    <button  type="submit">Add an agent</button>
  </form>
  <p  class="muted small">
    Put it in a channel. An agent shares what it makes the way it is shared
    itself, so one in no channel raises work only it can see. That is the
    honest consequence of channel membership being the permission model, and
    it is better said here than discovered next Tuesday.
  </p>

  <h2>Tokens</h2>
  {#if F.t(dot?.AgentTokens)}
    <table  class="list">
      <thead><tr><th>Token</th><th>Agent</th><th>Scopes</th><th>Last used</th><th></th></tr></thead>
      <tbody>
        {#each F.rng(dot?.AgentTokens) as [k4, d4]}
          <tr  data-token={S(d4?.ID)}>
            <td>{d4?.Name}</td>
            <td  class="muted small">{d4?.AgentName}</td>
            <td>{#each F.rng(d4?.Scopes) as [k5, d5]}<span  class="tag">{d5}</span> {/each}</td>
            <td  class="muted small">
              {#if F.t(d4?.LastUsedAt)}{F.ago(d4?.LastUsedAt)}{:else}never{/if}
              {#if F.t(d4?.ExpiresAt)} · expires {F.date(d4?.ExpiresAt)}{/if}
            </td>
            <td>
              <form  method="post"  action={"/agents/tokens/" + S(d4?.ID) + "/revoke"}  class="inline">
                <button  type="submit"  class="ghost small">Revoke</button>
              </form>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">No tokens.</p>
  {/if}

  {#if F.t(dot?.Agents)}
    <form  method="post"  action="/agents/tokens"  class="row wrap">
      <select  name="agent"  required aria-label="Agent">
        {#each F.rng(dot?.Agents) as [k6, d6]}<option  value={S(d6?.ID)}>{d6?.Name}</option>{/each}
      </select>
      <input  name="name"  placeholder="what it is for"  required aria-label="Token name">
      {#each F.rng(dot?.Scopes) as [k7, d7]}
        <label  class="row small">
          <input  type="checkbox"  name="scopes"  value={S(d7)}                   {...(F.t(F.eq(d7, "read")) ? {"checked": true} : {})}> {d7}
        </label>
      {/each}
      <input  name="days"  type="number"  min="1"  placeholder="days"  style="width:6rem"               aria-label="Expires in days">
      <button  type="submit">Mint a token</button>
    </form>
    <p  class="muted small">
      Scopes narrow a token below what its agent may do; they never widen it.
      A read token on an agent that can edit is read only, and the grants stay
      the ceiling either way.
    </p>
  {/if}

  {#if F.t(dot?.Decided)}
    <h2>Decided</h2>
    <ul  class="files"  data-decided>
      {#each F.rng(dot?.Decided) as [k8, d8]}
        <li>
          <span  class={"status " + S((F.t(F.eq(d8?.Status, "accepted")) ? ("done") : ("blocked")))}>{d8?.Status}</span>
          <strong>{d8?.Summary}</strong>
          <span  class="tag">{d8?.AgentName}</span>
          {#if F.t(d8?.DecidedBy)}<span  class="muted small">by {d8?.DecidedBy}</span>{/if}
          {#if F.t(d8?.Result)}<span  class="muted small">{d8?.Result}</span>{/if}
        </li>
      {/each}
    </ul>
  {/if}
</section>
{/snippet}
{@render content(dot)}
