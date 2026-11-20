<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<h1>{dot?.Team?.Name}</h1>
{#if F.t(dot?.Team?.Managed)}
  <p  class="muted">
    This team mirrors a group in your directory. Its name and its membership are
    changed there, and a sync will put back anything changed here.
  </p>
{:else if F.t(dot?.Team?.Description)}
  <p  class="muted">{dot?.Team?.Description}</p>
{/if}

{#if F.t(F.not(dot?.Team?.Managed))}
  <form  class="row"  method="post"  action={"/teams/" + S(dot?.Team?.ID) + "/rename"}>
    <label  for="name"  class="muted">Name</label>
    <input  id="name"  name="name"  value={S(dot?.Team?.Name)}  style="max-width:18rem">
    <button  type="submit">Rename</button>
  </form>
{/if}

<h2>People</h2>
{#if F.t(dot?.TeamMembers)}
  <ul  class="files touchlist"  data-team-members>
    {#each F.rng(dot?.TeamMembers) as [k1, d1]}
      <li  data-member={S(d1?.PrincipalID)}>
        <strong>{d1?.Name}</strong>
        {#if F.t(F.ne(d1?.Status, "active"))}<span  class="status blocked">{d1?.Status}</span>{/if}
        <span  class="muted small">since {F.date(d1?.AddedAt)}</span>
        <form  method="post"  action={"/teams/" + S(dot?.Team?.ID) + "/members/" + S(d1?.PrincipalID) + "/delete"}  class="inline">
          <button  type="submit"  class="ghost small">Remove</button>
        </form>
      </li>
    {/each}
  </ul>
{:else}
  <p  class="muted small">Nobody is in this team.</p>
{/if}

<form  class="row"  method="post"  action={"/teams/" + S(dot?.Team?.ID) + "/members"}>
  <label  for="principal"  class="muted">Add</label>
  <select  id="principal"  name="principal"  required>
    <option  value="">choose somebody</option>
    {#each F.rng(dot?.Persons) as [k2, d2]}<option  value={S(d2?.PrincipalID)}>{d2?.DisplayName}</option>{/each}
  </select>
  <button  type="submit">Add to the team</button>
</form>

<h2>What this team can see</h2>
<p  class="muted small">
  Asked as the team rather than as you, so everything listed is here because of
  the team and not because you can see everything.
</p>
{#if F.t(dot?.Documents)}
  <ul  class="files"  data-team-access>
    {#each F.rng(dot?.Documents) as [k3, d3]}
      <li>
        <a  href={("/documents/" + S(d3?.ID))}>{d3?.Title}</a>
        <span  class="tag">{d3?.Kind}</span>
      </li>
    {/each}
  </ul>
{:else}
  <p  class="muted small">Nothing has been shared with this team yet.</p>
{/if}

<form  class="row wrap"  method="post"  action={"/teams/" + S(dot?.Team?.ID) + "/grant"}>
  <label  for="document"  class="muted">Share</label>
  <select  id="document"  name="document"  required style="max-width:26rem">
    <option  value="">choose something</option>
    {#each F.rng(dot?.Assignable) as [k4, d4]}<option  value={S(d4?.ID)}>{d4?.Title}</option>{/each}
  </select>
  <select  name="level"  aria-label="Access">
    <option  value="view">can view</option>
    <option  value="comment">can comment</option>
    <option  value="edit">can edit</option>
    <option  value="admin">can administer</option>
  </select>
  <button  type="submit"  class="primary">Share with this team</button>
</form>
{/snippet}
{@render content(dot)}
