<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
  const v = $derived(dot?.SocialArea);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Social</h1>
  {#if F.t(v?.Notice)}<p  class="card small"  data-flash>{v?.Notice}</p>{/if}

  {#if v?.Configured}
    <nav  class="row tabs">
      <a  class={'chip' + (v.Tab === 'posts' ? ' on' : '')}  href="/social">Posts</a>
      <a  class={'chip' + (v.Tab === 'activity' ? ' on' : '')}  href="/social?tab=activity">Activity</a>
      <a  class={'chip' + (v.Tab === 'media' ? ' on' : '')}  href="/social?tab=media">Media</a>
    </nav>
  {/if}

  {#if !v?.Configured}
    <p  class="muted"  data-not-set-up>
      Social is not connected on this installation. Set <code>HEARTH_SOCIAL_URL</code> and <code>HEARTH_SOCIAL_API_KEY</code> to the
      address and API key of your social publishing app, and your channels and posts appear here.
    </p>
  {:else if v.Tab === 'activity'}
    <h2>Activity</h2>
    {#if v.Notifications && v.Notifications.length}
      <ul  class="list"  data-activity>{#each v.Notifications as n}<li>{n.Content}{#if n.Link} <a  href={n.Link}>Open</a>{/if} <span  class="muted small">{(n.CreatedAt ?? '').slice(0, 16).replace('T', ' ')}</span></li>{/each}</ul>
    {:else}<p  class="muted"  data-no-activity>Nothing from the publishing app yet.</p>{/if}
  {:else if v.Tab === 'media'}
    <h2>Media</h2>
    {#if v.MediaOff}
      <p  class="muted"  data-media-off>The publishing app on this installation has no media library, so there is nothing to list or add here.</p>
    {:else}
    <form  method="post"  action="/social/media"  class="row"  data-media-upload>
      <input  type="url"  name="url"  placeholder="Address of an image or video to add"  aria-label="Address of a file"  required>
      <button  type="submit">Add to the library</button>
    </form>
    {#if v.Media && v.Media.length}
      <ul  class="list"  data-media>{#each v.Media as m}<li><a  href={m.Path}  target="_blank"  rel="noopener">{m.OriginalName || m.Path}</a> <span  class="muted small">{(m.CreatedAt ?? '').slice(0, 10)}</span></li>{/each}</ul>
    {:else}<p  class="muted">No files in the media library.</p>{/if}
    {/if}
  {:else}
    <h2>Channels</h2>
    {#if v.Channels && v.Channels.length}
      <ul  class="list"  data-channels>
        {#each v.Channels as c}
          <li  data-channel={c.ID}><strong>{c.Name}</strong> <span  class="muted small">{c.Kind}{c.Handle ? ' · ' + c.Handle : ''}{c.Disabled ? ' · disabled' : ''}</span>
            <a  class="small"  href={"/social?channel=" + c.ID}>Numbers</a></li>
        {/each}
      </ul>
    {:else}
      <p  class="muted"  data-no-channels>No channel is connected yet. Connect one in your social publishing app, and it shows here.</p>
    {/if}

    {#if v.Metrics && v.Metrics.length}
      <table  class="list small"  data-metrics>
        <thead><tr><th>Last 30 days</th><th>Latest</th><th>Change</th></tr></thead>
        <tbody>{#each v.Metrics as m}<tr><td>{m.Label}</td><td>{(m.Data ?? []).slice(-1)[0]?.Total ?? ''}</td><td>{m.Change ?? ''}%</td></tr>{/each}</tbody>
      </table>
    {/if}

    {#if v.PostMetrics && v.PostMetrics.length}
      <table  class="list small"  data-post-metrics>
        <thead><tr><th>This post, last 30 days</th><th>Latest</th><th>Change</th></tr></thead>
        <tbody>{#each v.PostMetrics as m}<tr><td>{m.Label}</td><td>{(m.Data ?? []).slice(-1)[0]?.Total ?? ''}</td><td>{m.Change ?? ''}%</td></tr>{/each}</tbody>
      </table>
    {/if}

    {#if v.Channels && v.Channels.length}
      <form  method="post"  action="/social/post"  class="card stack"  data-compose>
        <h2>New post</h2>
        <fieldset>{#each v.Channels as c}{#if !c.Disabled}<label  class="small"><input  type="checkbox"  name="channel"  value={c.ID}> {c.Name} ({c.Kind})</label> {/if}{/each}</fieldset>
        <textarea  name="content"  rows="5"  aria-label="What to say"  required></textarea>
        <div  class="row">
          <select  name="when"  aria-label="When"><option  value="now">Publish now</option><option  value="schedule">Schedule</option><option  value="draft">Save as a draft</option></select>
          <input  type="datetime-local"  name="at"  aria-label="Publish at">
          <button  type="submit"  class="primary">Post</button>
        </div>
      </form>
    {/if}

    <h2>Posts</h2>
    {#if v.Posts && v.Posts.length}
      <table  class="list small"  data-posts>
        <thead><tr><th>When</th><th>Channel</th><th>Post</th><th>State</th><th></th></tr></thead>
        <tbody>
          {#each v.Posts as p}
            <tr  data-post={p.ID}>
              <td>{F.date(p.PublishDate)}</td><td>{p.Channel?.Name ?? p.Channel?.Kind}</td>
              <td>{p.Content}{#if p.URL} <a  href={p.URL}  target="_blank"  rel="noopener">View</a>{/if}</td>
              <td>{p.State === 'QUEUE' ? 'Scheduled' : p.State === 'PUBLISHED' ? 'Published' : p.State === 'ERROR' ? 'Failed' : 'Draft'}{#if p.Error} <span  class="err">{p.Error}</span>{/if}</td>
              <td  class="row">
                {#if p.State === 'QUEUE'}<form  method="post"  action={"/social/post/" + p.ID + "/status"}  class="inline"><input  type="hidden"  name="to"  value="draft"><button  class="ghost small">Make a draft</button></form>{/if}
                {#if p.State === 'DRAFT'}<form  method="post"  action={"/social/post/" + p.ID + "/status"}  class="inline"><input  type="hidden"  name="to"  value="schedule"><button  class="ghost small">Schedule</button></form>{/if}
                {#if p.State === 'PUBLISHED'}<a  class="small"  href={"/social?post=" + p.ID}>Numbers</a>{/if}
                <form  method="post"  action={"/social/post/" + p.ID + "/delete"}  class="inline"  data-confirm="Delete this post?"><button  class="ghost small">Delete</button></form>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}<p  class="muted">No posts in the last month or the next three.</p>{/if}
  {/if}
</section>
{/snippet}
{@render content(dot)}
