<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>People</h1>
  {#if F.t(dot?.Message)}<p  class="err">{dot?.Message}</p>{/if}

  <div  class="card small peoplehelp">
    <strong>How this page works</strong>
    <ul>
      <li>Click a name to see and change everything about that person, on one page.</li>
      <li><strong>Works in</strong> puts somebody on the org chart and decides who approves their hours. It does not give access to anything.</li>
      <li><strong>Can see work in</strong> is which departments' tasks they can open. <strong>Can open</strong> is which admin pages they get (Finance, Audit, People&hellip;). Two different things, even when the names match.</li>
    </ul>
  </div>

  <form  method="get"  action="/people"  class="row">
    <input  name="q"  value={S(dot?.SearchText)}  placeholder="Search by name or email"  aria-label="Search people"  style="max-width:20rem">
    <button  type="submit"  class="ghost small">Search</button>
    {#if F.t(dot?.SearchText)}<a  href={("/people")}  class="small">Show everybody</a>{/if}
  </form>

  {#if F.t(dot?.Persons)}
    <table  class="list">
      <thead>
        <tr><th>Name</th><th>Works in</th><th>Can see work in</th><th>Can open</th><th>Last seen</th></tr>
      </thead>
      <tbody>
        {#each F.rng(dot?.Persons) as [k1, d1]}{@const v_p = d1}{@const v_st = F.index(dot?.Chart, v_p?.PrincipalID)}{@const v_sees = F.index(dot?.SpaceGrants, v_p?.PrincipalID)}{@const v_held = F.index(dot?.Grants, v_p?.PrincipalID)}
          
          
          
          <tr>
            <td>
              <a  href={("/people/" + S(v_p?.PrincipalID))}><strong>{v_p?.DisplayName}</strong></a>
              {#if F.t(v_p?.IsOwner)} <span  class="tag">owner</span>{/if}
              {#if F.t(F.ne(v_p?.Status, "active"))} <span  class="tag over">{v_p?.Status}</span>{/if}
              <div  class="muted small">{#if F.t(v_p?.Email)}{v_p?.Email}{:else}single sign-on{/if}</div>
            </td>
            <td  class="small">
              {#if F.t(F.and(v_st, v_st?.Departments))}{#each F.rng(v_st?.Departments) as [k2, d2]}{@const v_i = k2}{@const v_d = d2}{#if F.t(v_i)}, {/if}{v_d?.Name}{/each}{:else}<span  class="muted">&mdash;</span>{/if}
            </td>
            <td  class="small">
              {#if F.t(v_sees)}{F.len(v_sees)} department{#if F.t(F.ne(F.len(v_sees), 1))}s{/if}
              {:else}<span  class="tag over"  title="This account can sign in but cannot see any department's work">nothing yet</span>{/if}
            </td>
            <td  class="small">
              {#if F.t(F.and(v_held, v_held?.IsSuperAdmin))}<span  class="tag">super admin</span>
              {:else}{#each F.rng(dot?.Grantable) as [k3, d3]}{#if F.t(F.and(v_held, F.index(v_held, d3)))}<span  class="tag">{d3?.Label}</span> {/if}{/each}{/if}
            </td>
            <td  class="muted small">{#if F.t(v_p?.LastLoginAt)}{F.ago(v_p?.LastLoginAt)}{:else}never{/if}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">Nobody matches &ldquo;{dot?.SearchText}&rdquo;.</p>
  {/if}

  <details  class="card">
    <summary><strong>Add somebody</strong></summary>
    <p  class="muted small">
      Leave the password blank and Hearth makes a link they open to choose their
      own, which is better than sending a password. If you do type one, they are
      made to change it the first time they sign in. Single sign-on accounts
      appear here on their own the first time they arrive.
    </p>
    <form  method="post"  action="/people"  class="stack">
      <label  for="name">Name</label>
      <input  id="name"  name="name"  autocomplete="off">
      <label  for="email">Email</label>
      <input  id="email"  name="email"  type="email"  required autocomplete="off">
      <label  for="password">First password (optional)</label>
      <input  id="password"  name="password"  type="password"               minlength="12"  autocomplete="new-password">
      <div  class="row"><button  type="submit"  class="primary">Add</button></div>
    </form>
  </details>
</section>
{/snippet}
{@render content(dot)}
