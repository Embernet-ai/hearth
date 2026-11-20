<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section  class="card">
  <h1>Your account</h1>
  {#if F.t(dot?.Identity)}
    
    <div  class="row avatarset">
      <img  class="avatar avatar-lg"  src={"/people/" + S(dot?.Identity?.PrincipalID) + "/avatar"}  alt="Your picture"  width="72"  height="72">
      <form  method="post"  action="/account/avatar"  enctype="multipart/form-data"  class="row">
        <label  for="avatar"  class="muted">Profile picture</label>
        <input  id="avatar"  type="file"  name="avatar"  accept="image/png,image/jpeg,image/gif,image/webp"  required>
        <button  type="submit">Upload</button>
      </form>
      <form  method="post"  action="/account/avatar/remove"  class="inline">
        <button  type="submit"  class="ghost small">Remove picture</button>
      </form>
    </div>
    <p  class="muted small">PNG, JPEG, GIF or WebP, up to 2 MB. A square picture of your face works best; it shows as a circle.</p>
  {/if}
  {#if F.t(dot?.MustChange)}
    <p  class="warn">
      This account is still on a password somebody else chose for it. Choose
      your own below before you carry on, because until you do, they know it
      too. Hearth will not open anything else until you have.
    </p>
  {/if}
  {#if F.t(dot?.Message)}<p  class="err">{dot?.Message}</p>{/if}

  <form  method="post"  action="/account/password"  class="stack">
    <label  for="current">Current password</label>
    <input  id="current"  name="current"  type="password"  required autocomplete="current-password">
    <label  for="next">New password</label>
    <input  id="next"  name="next"  type="password"  required minlength="12"             autocomplete="new-password">
    <p  class="muted small">
      Twelve characters at least. Length rather than a symbol rule, because a
      rule that demands punctuation produces Passw0rd! and nothing safer.
    </p>
    <div  class="row"><button  type="submit"  class="primary">Change password</button></div>
  </form>
</section>

{#if F.t(dot?.Identity)}
<section  class="card">
  <h2>Who you are here</h2>
  
  <p  class="muted small">Your identifier, which is what the audit log records
    and what a token acts as.</p>
  <p><code  data-principal={S(dot?.Identity?.PrincipalID)}>{dot?.Identity?.PrincipalID}</code></p>
  <p  class="muted small">
    <a  href={("/audit?actor=" + S(dot?.Identity?.PrincipalID))}>Everything you have done</a>
  </p>

  {#if F.t(dot?.GoogleOn)}
    <h3>Google</h3>
    {#if F.t(dot?.Google)}
      {#if F.t(dot?.Google?.Live)}
        <p  class="muted small">
          Connected as <strong>{dot?.Google?.Email}</strong> since {F.date(dot?.Google?.ConnectedAt)}.
          Hearth reads this as you, so it sees only what you can see, and
          disconnecting here or in your Google account ends it immediately.
        </p>
      {:else}
        <p  class="err">
          This connection was revoked in Google{#if F.t(dot?.Google?.LastError)}: {dot?.Google?.LastError}{/if}.
          Connect again to restore it.
        </p>
      {/if}
      <div  class="row">
        <form  method="post"  action="/integrations/google/sync"  class="inline">
          <button  type="submit"  class="ghost small">Sync my calendar now</button>
        </form>
        <form  method="post"  action="/integrations/google/disconnect"  class="inline"                data-confirm="Disconnect Google? Hearth forgets the connection and the events it pulled in.">
          <button  type="submit"  class="ghost small">Disconnect</button>
        </form>
      </div>
    {:else}
      
      <p  class="muted small">
        Connect your own Google account. Every request Hearth makes is made as
        you, so it sees only what you see, and nobody else can read your mail or
        your calendar through it.
      </p>
      <form  method="post"  action="/integrations/google/connect"  class="stack">
        <label><input  type="checkbox"  name="scope"            value="https://www.googleapis.com/auth/calendar.readonly"  checked>
          Read my calendar into Hearth</label>
        <label><input  type="checkbox"  name="scope"            value="https://www.googleapis.com/auth/calendar.events">
          Let Hearth put its events into my Google calendar</label>
        <label><input  type="checkbox"  name="scope"            value="https://www.googleapis.com/auth/gmail.readonly">
          Read my mail</label>
        <label><input  type="checkbox"  name="scope"            value="https://www.googleapis.com/auth/gmail.send">
          Send mail as me</label>
        <div  class="row"><button  type="submit"  class="primary">Connect Google</button></div>
      </form>
    {/if}
  {/if}
</section>
{/if}
{/snippet}
{@render content(dot)}
