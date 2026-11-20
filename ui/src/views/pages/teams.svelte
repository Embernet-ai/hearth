<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<h1>{dot?.Heading}</h1>
<p  class="muted">
  A team is a subject the grants already understood, so sharing a machine with
  a team is the same thing as sharing it with a person. Membership is resolved
  on every request, which is what makes taking somebody out of a team take
  effect on their next click rather than on their next sign-in.
</p>

<form  class="row"  method="post"  action="/teams">
  <input  name="name"  placeholder="Line 3 maintenance"  required style="max-width:18rem">
  <input  name="description"  placeholder="what this team is for"  style="max-width:22rem">
  <button  type="submit"  class="primary">Create a team</button>
</form>

{#if F.t(dot?.Teams)}
  <table  class="list"  data-teams>
    <thead><tr><th>Team</th><th>People</th><th>From</th><th></th></tr></thead>
    <tbody>
      {#each F.rng(dot?.Teams) as [k1, d1]}
        <tr  data-team={S(d1?.ID)}>
          <td>
            <a  href={("/teams/" + S(d1?.ID))}>{d1?.Name}</a>
            {#if F.t(d1?.Description)}<div  class="muted small">{d1?.Description}</div>{/if}
          </td>
          <td  data-members={S(d1?.Members)}>{d1?.Members}</td>
          <td>
            {#if F.t(d1?.Managed)}
              <span  class="tag">directory</span>
            {:else}
              <span  class="muted small">here</span>
            {/if}
          </td>
          <td>
            <form  method="post"  action={"/teams/" + S(d1?.ID) + "/delete"}  class="inline">
              <button  type="submit"  class="ghost small">Delete</button>
            </form>
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
{:else}
  <p  class="muted small">No teams yet.</p>
{/if}
{/snippet}
{@render content(dot)}
