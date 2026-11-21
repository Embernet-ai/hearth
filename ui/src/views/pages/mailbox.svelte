<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
  const mb = $derived(dot?.Mailbox);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Mailbox</h1>
  {#if F.t(mb?.Notice)}<p  class="card small"  data-flash>{mb?.Notice}</p>{/if}

  {#if !mb?.Providers || mb.Providers.length === 0}
    <p  class="muted">
      Nothing to open yet. An administrator turns on Mail for Microsoft 365 or Google Workspace, and you connect your
      account and tick Mail, under <a  href="/settings/connectors">Settings, Connectors</a>.
    </p>
  {:else}
    <form  method="get"  action="/mailbox"  class="row">
      <select  name="provider"  aria-label="Account">
        {#each mb.Providers as p}<option  value={p.Key}  selected={p.Key === mb.Provider}>{p.Label}</option>{/each}
      </select>
      <select  name="folder"  aria-label="Folder">
        <option  value="inbox"  selected={mb.Folder === 'inbox'}>Inbox</option>
        <option  value="sent"  selected={mb.Folder === 'sent'}>Sent</option>
        <option  value="archive"  selected={mb.Folder === 'archive'}>Archive</option>
        <option  value="all"  selected={mb.Folder === 'all'}>All mail</option>
      </select>
      <input  type="search"  name="q"  value={mb.Search}  placeholder="Search"  aria-label="Search mail">
      <button  type="submit"  class="ghost small">Open</button>
    </form>

    {#if mb.Open}
      <article  class="card"  data-message={mb.Open.ID}>
        <p><a  href={"/mailbox?provider=" + mb.Provider + "&folder=" + mb.Folder}>Back to the list</a></p>
        <h2>{mb.Open.Subject || '(no subject)'}</h2>
        <p  class="muted small">From {mb.Open.From} · to {(mb.Open.To ?? []).join(', ')}{#if mb.Open.Cc && mb.Open.Cc.length} · cc {mb.Open.Cc.join(', ')}{/if} · {F.date(mb.Open.Received)}</p>
        {#if mb.Open.Labels && mb.Open.Labels.length}<p  class="small">{#each mb.Open.Labels as l}<span  class="tag">{l}</span> {/each}</p>{/if}
        {#if mb.Open.Attached && mb.Open.Attached.length}<p  class="muted small">Attachments: {mb.Open.Attached.join(', ')}. <a  href={mb.Open.WebLink}  target="_blank"  rel="noopener">Open the message to download them</a>.</p>{/if}
        {#if mb.Open.HTML}
          <p  class="muted small">This message has no plain text version. <a  href={mb.Open.WebLink}  target="_blank"  rel="noopener">Open it in your mail</a> to see it formatted.</p>
        {:else}
          <pre  class="messagebody"  style="white-space: pre-wrap">{mb.Open.Body}</pre>
        {/if}

        <div  class="row">
          <form  method="post"  action="/mailbox/archive"  class="inline">
            <input  type="hidden"  name="provider"  value={mb.Provider}><input  type="hidden"  name="id"  value={mb.Open.ID}>
            <button  type="submit"  class="ghost small">Archive</button>
          </form>
          <form  method="post"  action="/mailbox/read"  class="inline">
            <input  type="hidden"  name="provider"  value={mb.Provider}><input  type="hidden"  name="id"  value={mb.Open.ID}>
            <input  type="hidden"  name="read"  value={mb.Open.Unread ? 'on' : 'off'}>
            <button  type="submit"  class="ghost small">{mb.Open.Unread ? 'Mark read' : 'Mark unread'}</button>
          </form>
          <form  method="post"  action="/mailbox/delete"  class="inline"  data-confirm="Move this message to the bin?">
            <input  type="hidden"  name="provider"  value={mb.Provider}><input  type="hidden"  name="id"  value={mb.Open.ID}>
            <button  type="submit"  class="ghost small">Delete</button>
          </form>
        </div>

        <form  method="post"  action="/mailbox/label"  class="row">
          <input  type="hidden"  name="provider"  value={mb.Provider}><input  type="hidden"  name="id"  value={mb.Open.ID}>
          <input  type="text"  name="add"  placeholder={'Add a label' + ((mb.Labels ?? []).length ? ' (' + mb.Labels.slice(0, 4).join(', ') + ')' : '')}  aria-label="Labels to add">
          <input  type="text"  name="remove"  placeholder="Remove a label"  aria-label="Labels to remove">
          <button  type="submit"  class="small">Label</button>
        </form>

        <form  method="post"  action="/mailbox/reply"  class="stack"  data-reply>
          <input  type="hidden"  name="provider"  value={mb.Provider}><input  type="hidden"  name="id"  value={mb.Open.ID}>
          <textarea  name="body"  rows="6"  aria-label="Reply"></textarea>
          <div  class="row">
            <label  class="small"><input  type="checkbox"  name="all"> Reply to everybody</label>
            <button  type="submit"  class="primary">Reply</button>
          </div>
        </form>

        <form  method="post"  action="/mailbox/forward"  class="stack"  data-forward>
          <input  type="hidden"  name="provider"  value={mb.Provider}><input  type="hidden"  name="id"  value={mb.Open.ID}>
          <input  type="text"  name="to"  placeholder="Forward to"  aria-label="Forward to">
          <textarea  name="body"  rows="3"  aria-label="Note to add above it"></textarea>
          <div  class="row"><button  type="submit">Forward</button></div>
        </form>
      </article>
    {:else}
      {#if mb.Messages && mb.Messages.length}
        <ul  class="list"  data-messages>
          {#each mb.Messages as m}
            <li  data-message={m.ID}  class={m.Unread ? 'unread' : ''}>
              <a  href={"/mailbox?provider=" + mb.Provider + "&folder=" + mb.Folder + "&id=" + encodeURIComponent(m.ID)}>{m.Subject || '(no subject)'}</a>
              <span  class="muted small">{m.From} · {F.date(m.Received)}</span>
              {#if m.Snippet}<div  class="muted small">{m.Snippet}</div>{/if}
            </li>
          {/each}
        </ul>
        {#if mb.Next}<p><a  href={"/mailbox?provider=" + mb.Provider + "&folder=" + mb.Folder + "&q=" + encodeURIComponent(mb.Search) + "&cursor=" + encodeURIComponent(mb.Next)}>Older messages</a></p>{/if}
      {:else}
        <p  class="muted">Nothing here.</p>
      {/if}

      <h2>New message</h2>
      <form  method="post"  action="/mailbox/send"  class="stack"  data-compose>
        <input  type="hidden"  name="provider"  value={mb.Provider}>
        <label>To <input  type="text"  name="to"  size="50"></label>
        <label>Cc <input  type="text"  name="cc"  size="50"></label>
        <label>Subject <input  type="text"  name="subject"  size="50"></label>
        <textarea  name="body"  rows="8"  aria-label="Message"></textarea>
        <div  class="row"><button  type="submit"  class="primary">Send</button></div>
      </form>
    {/if}
  {/if}
</section>
{/snippet}
{@render content(dot)}
