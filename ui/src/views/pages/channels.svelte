<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Channels</h1>
  <p  class="muted small">
    Joining a channel gives you everything attached to it. Leaving takes it
    away again. That is the whole permission model, and there is no request to
    approve in the middle of it.
  </p>
  {#if F.t(dot?.Message)}<p  class="err">{dot?.Message}</p>{/if}

  {#if F.t(dot?.Channels)}
    <table  class="list">
      <thead>
        <tr><th>Channel</th><th>Topic</th><th>People</th><th>Grants</th><th>Unread</th><th></th></tr>
      </thead>
      <tbody>
        {#each F.rng(dot?.Channels) as [k1, d1]}
          <tr>
            <td>
              <a  href={("/channels/" + S(d1?.ID))}>{d1?.Name}</a>
              {#if F.t(F.eq(d1?.Visibility, "private"))}<span  class="tag">private</span>{/if}
            </td>
            <td  class="muted small">{d1?.Topic}</td>
            <td  class="muted">{d1?.Members}</td>
            
            <td  class="muted">{d1?.Attached}</td>
            <td>{#if F.t(d1?.Unread)}<span  class="unread">{d1?.Unread}</span>{/if}</td>
            <td>
              {#if F.t(d1?.Member)}
                <form  method="post"  action={"/channels/" + S(d1?.ID) + "/leave"}  class="inline"                        data-confirm={"Leave " + S(d1?.Name) + "? You lose access to everything attached to it."}>
                  <button  type="submit"  class="ghost small">Leave</button>
                </form>
              {:else}
                <form  method="post"  action={"/channels/" + S(d1?.ID) + "/join"}  class="inline">
                  <button  type="submit"  class="ghost small">Join</button>
                </form>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">No channels yet.</p>
  {/if}

  <h3>New channel</h3>
  <form  method="post"  action="/channels"  class="stack">
    <label  for="name">Name</label>
    <input  id="name"  name="name"  required placeholder="Line 3 maintenance">
    <label  for="topic">Topic</label>
    <input  id="topic"  name="topic"  placeholder="What this channel is for">
    <label  for="visibility">Who can see it</label>
    <select  id="visibility"  name="visibility">
      <option  value="private">Private, members only</option>
      <option  value="public">Public, anybody here can join</option>
    </select>
    <div  class="row"><button  type="submit"  class="primary">Create</button></div>
  </form>
</section>
{/snippet}
{@render content(dot)}
