<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>
{#snippet timeline(dot)}
  {#if F.t(dot?.Messages)}
    <ul  class="messages">
      {#each F.rng(dot?.Messages) as [k1, d1]}
        <li  data-message={S(d1?.ID)}>
          <div  class="row spread">
            <span  class="author">{d1?.AuthorName}</span>
            <span  class="muted small">{F.ago(d1?.CreatedAt)}{#if F.t(d1?.EditedAt)} · edited{/if}</span>
          </div>
          <p  class="body">{d1?.Body}</p>
          {#if F.t(d1?.Mentions)}
            <p  class="mentions">
              {#each F.rng(d1?.Mentions) as [k2, d2]}
                {#if F.t(F.eq(F.printf("%s", d2?.Type), "document"))}
                  <a  class="chip"  href={("/documents/" + S(d2?.ID))}>{d2?.Label}</a>
                {:else if F.t(F.eq(F.printf("%s", d2?.Type), "channel"))}
                  <a  class="chip"  href={("/channels/" + S(d2?.ID))}>{d2?.Label}</a>
                {:else}
                  <span  class="chip">{d2?.Label}</span>
                {/if}
              {/each}
            </p>
          {/if}
        </li>
      {/each}
    </ul>
  {:else}
    <p  class="muted">Nothing said here yet.</p>
  {/if}
{/snippet}

{#snippet content(dot: any)}
<section  class="channelpage">
  <div  class="row spread">
    <div>
      <h1>{dot?.Channel?.Name}</h1>
      {#if F.t(dot?.Channel?.Topic)}<p  class="muted small">{dot?.Channel?.Topic}</p>{/if}
    </div>
    <div  class="row">
      {#if F.t(F.eq(dot?.Channel?.Visibility, "private"))}<span  class="tag">private</span>{/if}
      {#if F.t(dot?.Channel?.Member)}
        <form  method="post"  action={"/channels/" + S(dot?.Channel?.ID) + "/leave"}  class="inline"                data-confirm={"Leave " + S(dot?.Channel?.Name) + "? You lose access to everything attached to it."}>
          <button  type="submit"  class="ghost small">Leave</button>
        </form>
      {:else}
        <form  method="post"  action={"/channels/" + S(dot?.Channel?.ID) + "/join"}  class="inline">
          <button  type="submit"  class="primary">Join</button>
        </form>
      {/if}
    </div>
  </div>

  <div  class="channelgrid">
    <div>
      <div  id="timeline"  data-live>
        {@render timeline(dot)}
      </div>

      {#if F.t(dot?.Channel?.Member)}
        <form  method="post"  action={"/channels/" + S(dot?.Channel?.ID) + "/messages"}  class="stack composer">
          <textarea  name="body"  rows="3"  required placeholder="Say something. Paste a link to a work order and it will remember."></textarea>
          <div  class="row">
            <button  type="submit"  class="primary">Send</button>
            <span  class="muted small">
              @somebody by handle, or paste an address to link a work order.
            </span>
          </div>
        </form>
      {:else}
        <p  class="muted">Join the channel to post in it.</p>
      {/if}
    </div>

    <aside  class="sidebar">
      <h3>People</h3>
      <ul  class="plain">
        {#each F.rng(dot?.ChannelMembers) as [k3, d3]}
          <li>{d3?.Name}{#if F.t(F.eq(d3?.Role, "admin"))} <span  class="tag">admin</span>{/if}</li>
        {/each}
      </ul>

      {#if F.t(dot?.Channel?.Member)}
        <form  method="post"  action={"/channels/" + S(dot?.Channel?.ID) + "/invite"}  class="stack">
          <label  for="principal">Add somebody</label>
          <select  id="principal"  name="principal">
            {#each F.rng(dot?.People) as [k4, d4]}<option  value={S(d4?.ID)}>{d4?.Name}</option>{/each}
          </select>
          <button  type="submit"  class="ghost small">Add</button>
        </form>
      {/if}

      <h3>What this grants</h3>
      {#if F.t(dot?.AttachedDocs)}
        <ul  class="plain">
          {#each F.rng(dot?.AttachedDocs) as [k5, d5]}
            <li>
              <a  href={("/documents/" + S(d5?.ID))}>{#if F.t(d5?.Title)}{d5?.Title}{:else}Untitled{/if}</a>
              <span  class="tag">{d5?.Kind}</span>
            </li>
          {/each}
        </ul>
      {:else}
        <p  class="muted small">
          Nothing attached yet. Attach a document from its own page and everybody
          in here can see it.
        </p>
      {/if}
    </aside>
  </div>
</section>
{/snippet}
{@render content(dot)}
