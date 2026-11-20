<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Email</h1>

  {#if F.t(dot?.Message)}<p  class="card small"  data-flash>{dot?.Message}</p>{/if}

  <h2>Sending</h2>
  <p  class="muted small">
    How notifications leave Hearth under your own name. Without this, the
    in-app inbox still works but no email is sent &mdash; people only see
    assignments and mentions when they open Hearth.
  </p>

  <p  class="warn small"  data-admin-only>
    <strong>Admin / IT only.</strong> These are your email provider's server
    settings. If you are not sure what they are, ask whoever manages your
    company email rather than guessing &mdash; a wrong value here just means
    mail does not send, and the inbox keeps working in the meantime.
  </p>

  <details  class="rowedit"  data-mailhelp>
    <summary  class="ghost small">Not sure what to enter? Common setups</summary>
    <div  class="editbody">
      <p  class="muted small">
        Pick whoever sends your email. Use the <strong>Send test</strong>
        button below to confirm it before trusting it.
      </p>
      <table  class="list small">
        <thead>
          <tr><th>Provider</th><th>Host</th><th>Port</th><th>Username</th><th>Password</th></tr>
        </thead>
        <tbody>
          <tr>
            <td>Google Workspace / Gmail</td>
            <td><code>smtp.gmail.com</code></td>
            <td>587</td>
            <td>your full email address</td>
            <td>an <a  href="https://support.google.com/accounts/answer/185833"  target="_blank"  rel="noopener">app password</a>, not your login</td>
          </tr>
          <tr>
            <td>Microsoft 365 / Outlook</td>
            <td><code>smtp.office365.com</code></td>
            <td>587</td>
            <td>your full email address</td>
            <td>your mailbox password (or app password)</td>
          </tr>
          <tr>
            <td>SendGrid</td>
            <td><code>smtp.sendgrid.net</code></td>
            <td>587</td>
            <td>the literal word <code>apikey</code></td>
            <td>your SendGrid API key</td>
          </tr>
          <tr>
            <td>Mailgun</td>
            <td><code>smtp.mailgun.org</code></td>
            <td>587</td>
            <td>your Mailgun SMTP login</td>
            <td>your Mailgun SMTP password</td>
          </tr>
        </tbody>
      </table>
      <p  class="muted small">
        <strong>From address</strong> is who the email appears to come from
        (e.g. <code>hearth@yourcompany.com</code>) &mdash; it usually has to
        match the account above. Leave <strong>STARTTLS</strong> on unless your
        provider says otherwise.
      </p>
    </div>
  </details>

  {#if F.t(dot?.OutboundMail)}{@const w1 = dot?.OutboundMail}
    <form  method="post"  action="/settings/mail/outbound"  class="stack"  style="max-width:34rem">
      <label  class="check">
        <input  type="checkbox"  name="enabled"  {...(F.t(w1?.Enabled) ? {"checked": true} : {})}>
        Send email through this relay
      </label>
      <label>Host
        <input  name="host"  value={S(w1?.Host)}  placeholder="smtp.example.com"  style="width:100%">
      </label>
      <label>Port
        <input  name="port"  type="number"  min="1"  max="65535"  value={S(w1?.Port)}  style="width:8rem">
      </label>
      <label>From address
        <input  name="from_address"  type="email"  value={S(w1?.From)}  placeholder="hearth@example.com"  style="width:100%">
      </label>
      <label>From name <span  class="muted small">(optional)</span>
        <input  name="from_name"  value={S(w1?.FromName)}  placeholder="Acme Hearth"  style="width:100%">
      </label>
      <label>Username <span  class="muted small">(optional)</span>
        <input  name="username"  value={S(w1?.Username)}  autocomplete="off"  style="width:100%">
      </label>
      {#if F.t(dot?.CanStorePassword)}
        <label>Password
          <input  name="password"  type="password"  autocomplete="new-password"                   placeholder={S((F.t(w1?.HasPassword) ? ("•••••••• (unchanged)") : ("none set")))}  style="width:100%">
        </label>
        {#if F.t(w1?.HasPassword)}
          <label  class="check">
            <input  type="checkbox"  name="clear_password"> Remove the stored password
          </label>
        {/if}
      {:else}
        <p  class="warn small"  data-no-key>
          This deployment has no encryption key, so a relay password cannot be
          stored. Set HEARTH_ENCRYPTION_KEY to use a relay that needs a password.
        </p>
      {/if}
      <label  class="check">
        <input  type="checkbox"  name="starttls"  {...(F.t(w1?.StartTLS) ? {"checked": true} : {})}> Use STARTTLS
      </label>
      <div  class="row">
        <button  type="submit"  class="primary">Save</button>
      </div>
    </form>

    <form  method="post"  action="/settings/mail/outbound/test"  class="row wrap"  style="margin-top:.75rem">
      <input  name="to"  type="email"  placeholder="you@example.com"  required style="width:18rem"  aria-label="Send a test to">
      <button  type="submit"  class="ghost small">Send test</button>
      <span  class="muted small">Sends one message through the settings above (save first).</span>
    </form>
  {/if}

  <h2>Receiving</h2>
  <p  class="muted small">
    Work arrives by email whether or not the software is ready for it. One
    address per space means where a message lands is a property of the address
    it was sent to, so the crew tell each other an address instead of being
    trained on a tool.
  </p>

  {#if F.t(F.not(dot?.InboundReady))}
    <p  class="warn"  data-inbound-off>
      Inbound mail is switched off. Set HEARTH_INBOUND_KEY and have the relay
      POST each message to /inbound/mail with that key in the
      X-Hearth-Inbound-Key header. Until then the addresses below are
      configuration nothing will use.
    </p>
  {/if}
  {#if F.t(F.not(dot?.MailReady))}
    <p  class="warn"  data-relay-off>
      No relay is configured, so replies will be recorded on the work order and
      go nowhere. Set HEARTH_SMTP_HOST and HEARTH_SMTP_FROM.
    </p>
  {/if}

  {#if F.t(dot?.InboundAddresses)}
    <table  class="list">
      <thead>
        <tr><th>Address</th><th>Becomes</th><th>Space</th><th>Channel</th><th></th></tr>
      </thead>
      <tbody>
        {#each F.rng(dot?.InboundAddresses) as [k2, d2]}
          <tr  data-inbound={S(d2?.ID)}>
            <td>
              <code>{d2?.Address}</code>
              {#if F.t(F.not(d2?.Enabled))}<span  class="tag">off</span>{/if}
            </td>
            <td><span  class="tag">{d2?.Kind}</span></td>
            <td  class="muted small">{d2?.SpaceName}</td>
            <td  class="muted small">{#if F.t(d2?.ChannelID)}{F.short(F.deref_s(d2?.ChannelID))}{/if}</td>
            <td>
              <form  method="post"  action={"/settings/mail/" + S(d2?.ID) + "/delete"}  class="inline">
                <button  type="submit"  class="ghost small">Remove</button>
              </form>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">No inbound addresses yet.</p>
  {/if}

  <h2>Add an address</h2>
  <form  method="post"  action="/settings/mail"  class="row wrap">
    <input  name="address"  placeholder="maintenance@example.com"  required style="width:16rem"  aria-label="Address">
    <select  name="kind"  aria-label="Becomes">
      <option  value="work_order">a work order</option>
      <option  value="task">a task</option>
      <option  value="note">a note</option>
    </select>
    <select  name="space"  aria-label="Space">
      <option  value="">No space</option>
      {#each F.rng(dot?.Spaces) as [k3, d3]}<option  value={S(d3?.ID)}>{d3?.Path}</option>{/each}
    </select>
    <select  name="channel"  aria-label="Channel">
      <option  value="">No channel</option>
      {#each F.rng(dot?.Channels) as [k4, d4]}<option  value={S(d4?.ID)}>{d4?.Name}</option>{/each}
    </select>
    <button  type="submit">Add</button>
  </form>
  <p  class="muted small">
    Attaching a channel is how the crew see what arrives without anybody
    sharing each message: channel membership is already the permission model.
  </p>

  <h2>What the relay needs</h2>
  <pre  class="schema">postfix, one transport, one line of aliases:

  maintenance: "|/usr/bin/curl -sS --fail-with-body -X POST \
      -H 'X-Hearth-Inbound-Key: $HEARTH_INBOUND_KEY' \
      --data-binary @- {dot?.ScanURL}"</pre>
  <p  class="muted small">
    An HTTP endpoint rather than an SMTP listener, because the estate already
    runs a relay and it is better at SMTP than anything written here would be.
    This application never listens on port 25, which is one fewer thing to
    patch. A non-2xx makes the relay retry, and the Message-ID means a retry
    cannot produce a second work order.
  </p>
</section>
{/snippet}
{@render content(dot)}
