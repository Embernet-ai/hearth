<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <p  class="small"><a  href="/spaces">&larr; Spaces</a></p>
  <h1>Access to {dot?.AccessSpace?.Name}</h1>
  <p  class="muted small">{dot?.AccessSpace?.Path}</p>
  <p  class="muted small">
    Give one person this folder and everything inside it: its folders, documents
    and tasks. They see nothing above this folder and nothing beside it, and the
    folders above are not named to them. A person you give it to who is not part of the
    company is best on a contractor account, which an admin sets on the person's page.
  </p>

  {#if F.t(dot?.Message)}<p  class="card small"  role="alert">{dot?.Message}</p>{/if}

  <div  class="card">
    <h3>Give access</h3>
    <form  method="post"  action={"/spaces/" + S(dot?.AccessSpace?.ID) + "/access"}  class="row">
      <label  class="small">Email they sign in with
        <input  name="email"  type="email"  required  autocomplete="off"  placeholder="name@example.com">
      </label>
      <label  class="small">Can
        <select  name="level">
          <option  value="view">View</option>
          <option  value="comment">View and comment</option>
          <option  value="edit">View, comment and edit</option>
        </select>
      </label>
      <label  class="small">Last day (optional)
        <input  name="expires"  type="date">
      </label>
      <button  type="submit">Give access</button>
    </form>
    <p  class="muted small">Editing includes adding and changing tasks and documents in this folder. A last day ends access at the end of that day, Eastern time.</p>
  </div>

  <div  class="card">
    <h3>Who has it</h3>
    {#if F.t(dot?.Delegations)}
      <table  class="small">
        <thead><tr><th>Person</th><th>Can</th><th>Until</th><th></th></tr></thead>
        <tbody>
          {#each F.rng(dot?.Delegations) as [k1, d1]}
            <tr>
              <td>{d1?.Name}{#if F.t(d1?.Email)} <span  class="muted">{d1?.Email}</span>{/if}
                {#if F.t(d1?.Contractor)} <span  class="tag">contractor</span>{/if}</td>
              <td>{#if F.t(F.eq(d1?.Level, "edit"))}Edit{:else if F.t(F.eq(d1?.Level, "comment"))}Comment{:else}View{/if}</td>
              <td>{#if F.t(d1?.Expired)}<span  class="tag over">ended</span>{:else if F.t(d1?.ExpiresAt)}{F.date(d1?.ExpiresAt)}{:else}no end date{/if}</td>
              <td>
                <form  method="post"  action={"/spaces/" + S(dot?.AccessSpace?.ID) + "/access/" + S(d1?.PersonID) + "/remove"}  class="inline"
                       data-confirm={"Take " + S(d1?.Name) + " off " + S(dot?.AccessSpace?.Name) + "? They stop seeing it straight away."}>
                  <button  type="submit"  class="ghost small danger">Take back</button>
                </form>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p  class="muted small">Nobody has been given this folder on its own.</p>
    {/if}
    <p  class="muted small">Only access given here is listed. People who reach this folder because they manage or belong to a department above it, or because an admin gave them a department, are not.</p>
  </div>
</section>
{/snippet}
{@render content(dot)}
