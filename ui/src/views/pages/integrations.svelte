<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Integrations</h1>
  <p  class="muted small">
    The API has no privileged back door. A token acts as one person, and every
    endpoint calls the same service method this interface calls with that
    person's permissions, so it refuses exactly what the pages refuse for them.
  </p>

  {#if F.t(dot?.NewToken)}
    <div  class="card"  data-new-token>
      <h3>Here is the token</h3>
      <p  class="muted small">
        Once, and there is nowhere to read it back from. A token recoverable
        from a page is a token that leaks through a screen share.
      </p>
      <pre  class="schema">{dot?.NewToken}</pre>
      <p  class="muted small">
        <code>curl -H "Authorization: Bearer &lt;token&gt;" {dot?.APIBase}/documents</code>
      </p>
    </div>
  {/if}

  <h2>API tokens</h2>
  {#if F.t(dot?.APITokens)}
    <table  class="list">
      <thead><tr><th>Token</th><th>Acts as</th><th>Scopes</th><th>Last used</th><th></th></tr></thead>
      <tbody>
        {#each F.rng(dot?.APITokens) as [k1, d1]}
          <tr  data-api-token={S(d1?.ID)}>
            <td>{d1?.Name}</td>
            <td  class="muted small">{d1?.OwnerName}</td>
            <td>{#each F.rng(d1?.Scopes) as [k2, d2]}<span  class="tag">{d2}</span> {/each}</td>
            <td  class="muted small">
              {#if F.t(d1?.LastUsedAt)}{F.ago(d1?.LastUsedAt)}{:else}never{/if}
              {#if F.t(d1?.ExpiresAt)} · expires {F.date(d1?.ExpiresAt)}{/if}
            </td>
            <td>
              <form  method="post"  action={"/integrations/tokens/" + S(d1?.ID) + "/revoke"}  class="inline">
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

  <form  method="post"  action="/integrations/tokens"  class="row wrap">
    <input  name="name"  placeholder="what it is for"  required aria-label="Token name">
    <select  name="principal"  aria-label="Acts as">
      <option  value="">Acts as you</option>
      {#each F.rng(dot?.People) as [k3, d3]}<option  value={S(d3?.ID)}>{d3?.Name}</option>{/each}
    </select>
    {#each F.rng(dot?.APIScopes) as [k4, d4]}
      <label  class="row small">
        <input  type="checkbox"  name="scopes"  value={S(d4)}                 {...(F.t(F.eq(d4, "read")) ? {"checked": true} : {})}> {d4}
      </label>
    {/each}
    <input  name="days"  type="number"  min="1"  placeholder="days"  style="width:6rem"             aria-label="Expires in days">
    <button  type="submit">Mint a token</button>
  </form>
  <p  class="muted small">
    Somebody who leaves and is suspended takes their tokens with them, because
    the principal's status is checked on every call rather than cached.
  </p>

  <h2>Webhooks</h2>
  <p  class="muted small">
    Fed by the outbox, so a delivery describes something that definitely
    committed. Every body is signed with a timestamp inside the signature, so
    an old one cannot be replayed by anybody who captured it.
  </p>
  {#if F.t(dot?.Hooks)}
    {#each F.rng(dot?.Hooks) as [k5, d5]}
      <div  class="card"  data-webhook={S(d5?.ID)}>
        <div  class="row spread">
          <code>{d5?.URL}</code>
          <div  class="row">
            {#if F.t(F.not(d5?.Enabled))}<span  class="tag">off</span>{/if}
            <form  method="post"  action={"/integrations/webhooks/" + S(d5?.ID) + "/delete"}  class="inline">
              <button  type="submit"  class="ghost small">Remove</button>
            </form>
          </div>
        </div>
        <p  class="muted small">
          Signing secret <code>{d5?.Secret}</code>.
          {#if F.t(d5?.Events)}Events: {F.join(d5?.Events)}.{:else}All events.{/if}
        </p>
        {#if F.t(d5?.Recent)}
          <ul  class="files"  data-deliveries>
            {#each F.rng(d5?.Recent) as [k6, d6]}
              <li>
                <span  class={"status " + S((F.t(d6?.OK) ? ("done") : ("blocked")))}>{d6?.Status}</span>
                <span  class="muted small">{d6?.Event}</span>
                {#if F.t(d6?.Code)}<span  class="muted small">HTTP {d6?.Code}</span>{/if}
                <span  class="muted small">{d6?.Attempts} attempt{#if F.t(F.ne(d6?.Attempts, 1))}s{/if}</span>
                <span  class="muted small">{F.ago(d6?.CreatedAt)}</span>
                {#if F.t(d6?.LastError)}<span  class="muted small">{d6?.LastError}</span>{/if}
              </li>
            {/each}
          </ul>
        {:else}
          <p  class="muted small">Nothing delivered yet.</p>
        {/if}
      </div>
    {/each}
  {:else}
    <p  class="muted">No webhooks.</p>
  {/if}

  <form  method="post"  action="/integrations/webhooks"  class="row wrap">
    <input  name="url"  type="url"  placeholder="https://example.com/hearth"             required style="width:20rem"  aria-label="URL">
    {#each F.rng(F.list("created", "updated", "deleted", "status_changed", "assigned")) as [k7, d7]}{@const v_e = d7}
      <label  class="row small">
        <input  type="checkbox"  name="events"  value={S(v_e)}> {v_e}
      </label>
    {/each}
    <button  type="submit">Add</button>
  </form>
  <p  class="muted small">
    Tick nothing to receive everything. A delivery that keeps failing lands in
    the dead letter table with the rest of the queue, rather than in a second
    retry mechanism written specially for webhooks.
  </p>

  <h2>Leaving another tool</h2>
  <p  class="muted small">
    Jira, Asana, Monday, Trello, and plain CSV, all through one engine and all
    re-runnable. Every row that carries an id is matched on it, so running the
    same export twice updates rather than duplicates, which matters because an
    import is never right the first time.
  </p>
  <p  class="muted small">
    <a  href={("/import")}>Import a file</a> ·
    <a  href={(S(dot?.APIBase) + "/export?format=csv")}>Export everything as CSV</a> ·
    <a  href={(S(dot?.APIBase) + "/export?format=json")}>as JSON</a>
  </p>
  <p  class="muted small">
    The export writes the columns the generic importer reads, so the file goes
    back in. An export its own product cannot re-import is a file rather than
    an exit.
  </p>
</section>

<section>
  <h2>Provisioning from your directory</h2>
  <p  class="muted small">
    Point Entra, Okta, or anything else that speaks SCIM at
    <code>{dot?.SCIMBase}</code> with one of these as the bearer token. Groups
    become teams, people become accounts, and taking somebody out of a group
    takes their access away on their next request rather than on their next
    sign-in.
  </p>
  <p  class="muted small">
    This token acts as the directory rather than as a person. It can create and
    suspend accounts and edit teams, and it cannot read a work order.
  </p>
  <form  class="row"  method="post"  action="/integrations/provisioning">
    <input  name="name"  placeholder="Entra provisioning"  required style="max-width:18rem">
    <button  type="submit">Issue a provisioning token</button>
  </form>
  {#if F.t(dot?.SCIMTokens)}
    <ul  class="files touchlist"  data-scim-tokens>
      {#each F.rng(dot?.SCIMTokens) as [k8, d8]}
        <li  data-scim-token={S(d8?.ID)}>
          <strong>{d8?.Name}</strong>
          <span  class="muted small">
            issued {F.date(d8?.CreatedAt)}{#if F.t(d8?.LastUsedAt)} · last used {F.ago(d8?.LastUsedAt)}{:else} · never used{/if}
          </span>
          <form  method="post"  action={"/integrations/provisioning/" + S(d8?.ID) + "/revoke"}  class="inline">
            <button  type="submit"  class="ghost small">Revoke</button>
          </form>
        </li>
      {/each}
    </ul>
  {:else}
    <p  class="muted small">No directory is connected.</p>
  {/if}
</section>
{/snippet}
{@render content(dot)}
