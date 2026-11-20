<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <div  class="row spread">
    <h1>Inbox</h1>
    {#if F.t(dot?.Notifications)}
      <form  method="post"  action="/inbox/read"  class="inline">
        <button  type="submit">Mark all read</button>
      </form>
    {/if}
  </div>
  {#if F.t(dot?.Message)}<p  class="err">{dot?.Message}</p>{/if}

  <nav  class="filters">
    <a  class={"chip " + S((F.t(dot?.UnreadOnly) ? ("on") : ""))}  href={("/inbox")}>Unread</a>
    <a  class={"chip " + S((F.t(F.not(dot?.UnreadOnly)) ? ("on") : ""))}  href={("/inbox?all=1")}>Everything</a>
  </nav>

  {#if F.t(dot?.Notifications)}
    <ul  class="hits">
      {#each F.rng(dot?.Notifications) as [k1, d1]}
        <li  data-notification={S(d1?.ID)}  class={S((F.t(F.not(d1?.ReadAt)) ? ("unreadrow") : ""))}>
          <a  href={(S(d1?.Href))}>{d1?.Sentence}</a>
          <span  class="tag">{d1?.Reason}</span>
          {#if F.t(d1?.Excerpt)}<p  class="snippet">{d1?.Excerpt}</p>{/if}
          <span  class="muted small">{F.ago(d1?.CreatedAt)}</span>
          {#if F.t(F.not(d1?.ReadAt))}
            <form  method="post"  action="/inbox/read"  class="inline">
              <input  type="hidden"  name="id"  value={S(d1?.ID)}>
              <button  type="submit"  class="ghost small">Read</button>
            </form>
          {/if}
        </li>
      {/each}
    </ul>
  {:else}
    <p  class="muted">
      {#if F.t(dot?.UnreadOnly)}Nothing unread.{:else}Nothing yet.{/if}
    </p>
  {/if}

  <h3  id="notifications">How often you hear from this</h3>
  <p  class="muted small">
    The point of these settings is that nobody has to turn the whole thing off.
    Quiet hours hold email until the morning rather than dropping it.
  </p>
  <form  method="post"  action="/inbox/prefs"  class="fields">
    <label  for="email">Email</label>
    <select  id="email"  name="email">
      <option  value="digest"  {...(F.t(F.eq(dot?.Prefs?.Email, "digest")) ? {"selected": true} : {})}>Once a day</option>
      <option  value="immediate"  {...(F.t(F.eq(dot?.Prefs?.Email, "immediate")) ? {"selected": true} : {})}>As it happens</option>
      <option  value="off"  {...(F.t(F.eq(dot?.Prefs?.Email, "off")) ? {"selected": true} : {})}>Never</option>
    </select>

    <label  for="digest_at">Daily at</label>
    <input  id="digest_at"  name="digest_at"  type="time"  value={S(F.clock(dot?.Prefs?.DigestAt))}>

    <label  for="quiet_from">Quiet from</label>
    <input  id="quiet_from"  name="quiet_from"  type="time"  value={S(F.clockp(dot?.Prefs?.QuietFrom))}>

    <label  for="quiet_to">Quiet until</label>
    <input  id="quiet_to"  name="quiet_to"  type="time"  value={S(F.clockp(dot?.Prefs?.QuietTo))}>

    <label  for="timezone">Your time zone</label>
    <input  id="timezone"  name="timezone"  value={S(dot?.Prefs?.Timezone)}  placeholder="Europe/London">

    <div  class="row"><button  type="submit"  class="primary">Save</button></div>
  </form>
</section>
{/snippet}
{@render content(dot)}
