<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Connectors</h1>
  <p  class="muted small">
    Connect Hearth to a Microsoft 365 business tenant or a Google Workspace domain. Each connector is off until an
    administrator turns it on, and each person connects their own account, so Hearth only ever sees what that person
    can see.
  </p>

  {#if F.t(dot?.Connectors?.Notice)}<p  class="card small"  data-flash>{dot?.Connectors?.Notice}</p>{/if}

  {#each (dot?.Connectors?.Providers ?? []) as p}
    <div  class="card"  data-provider={p.Key}>
      <h2>{p.Label}</h2>

      {#if !p.Configured}
        <p  class="muted small">
          This installation has no {p.Label} client set up, so the connectors for it are off. The person who installed
          Hearth adds one in the deployment settings; the setup page for {p.Label} lists exactly what to create.
        </p>
      {:else}
        {#if dot?.Connectors?.Admin}
          <h3>Tenant</h3>
          <form  method="post"  action="/settings/connectors/tenant"  class="row">
            <input  type="hidden"  name="provider"  value={p.Key}>
            <input  type="text"  name="tenant"  value={p.Tenant}  size="40"  aria-label={p.Label + " tenant"}  placeholder={p.TenantHelp}>
            <button  type="submit">{p.Tenant ? 'Change' : 'Connect'}</button>
          </form>
          <p  class="muted small">{p.TenantHelp} Only accounts in it can connect.</p>
        {:else if p.Tenant}
          <p  class="muted small">Connected to <code>{p.Tenant}</code>.</p>
        {/if}

        <h3>What is on</h3>
        <ul  class="list">
          {#each p.Connectors as c}
            <li  data-connector={c.Key}>
              <div  class="row">
                <strong>{c.Label}</strong>
                <span  class="muted small">{c.Unavailable ? 'Not available' : c.Enabled ? 'On' : 'Off'}</span>
                {#if dot?.Connectors?.Admin && !c.Unavailable}
                  {#if c.Enabled}
                    {#if c.Key === 'calendar'}
                      <form  method="post"  action="/settings/connectors/toggle"  class="inline row">
                        <input  type="hidden"  name="provider"  value={p.Key}>
                        <input  type="hidden"  name="connector"  value={c.Key}>
                        <input  type="hidden"  name="enabled"  value="on">
                        <label  class="small"><input  type="checkbox"  name="meet"  checked={c.MeetLinks}>
                          Add a {p.Key === 'google' ? 'Meet' : 'Teams'} link to events Hearth creates</label>
                        <button  type="submit"  class="ghost small">Save</button>
                      </form>
                    {/if}
                    <form  method="post"  action="/settings/connectors/toggle"  class="inline">
                      <input  type="hidden"  name="provider"  value={p.Key}>
                      <input  type="hidden"  name="connector"  value={c.Key}>
                      <button  type="submit"  class="ghost small">Turn off</button>
                    </form>
                  {:else}
                    <form  method="post"  action="/settings/connectors/toggle"  class="inline">
                      <input  type="hidden"  name="provider"  value={p.Key}>
                      <input  type="hidden"  name="connector"  value={c.Key}>
                      <input  type="hidden"  name="enabled"  value="on">
                      <button  type="submit"  class="small">Turn on</button>
                    </form>
                  {/if}
                {/if}
              </div>
              <p  class="muted small">{c.Describes}</p>
              {#if c.Unavailable}<p  class="muted small"  data-unavailable>{c.Unavailable}</p>{/if}
              {#if dot?.Connectors?.Admin && c.Enabled}
                <p  class="muted small">
                  {c.Connections} connected{#if c.Failing}, <span  class="err">{c.Failing} failing</span>{/if}
                  {#if c.LastOK}, last good run {F.ago(c.LastOK)}{/if}.
                </p>
              {/if}
            </li>
          {/each}
        </ul>

        <h3>Your account</h3>
        {#if p.Mine}
          {#if p.Mine.Live}
            <p  class="muted small">Connected as <strong>{p.Mine.Email}</strong> since {F.date(p.Mine.Since)}.</p>
          {:else}
            <p  class="err">This connection was revoked{#if p.Mine.LastError}: {p.Mine.LastError}{/if}. Connect again to restore it.</p>
          {/if}
          <div  class="row">
            <form  method="post"  action="/settings/connectors/sync"  class="inline">
              <input  type="hidden"  name="provider"  value={p.Key}>
              <button  type="submit"  class="ghost small">Sync now</button>
            </form>
            <form  method="post"  action="/settings/connectors/disconnect"  class="inline"  data-confirm={"Disconnect " + p.Label + "? Hearth forgets the connection. Events and contacts it brought in stay."}>
              <input  type="hidden"  name="provider"  value={p.Key}>
              <button  type="submit"  class="ghost small">Disconnect</button>
            </form>
          </div>
        {/if}
        {#if p.Mine && p.Mine.Live}
          <p  class="small">
            {#each p.Connectors as c}
              {#if c.Key === 'mail' && c.Enabled && c.Covered}<a  href={"/mailbox?provider=" + p.Key}>Open your mailbox</a> {/if}
              {#if c.Key === 'files' && c.Enabled && c.Covered}<a  href={"/cloud?provider=" + p.Key}>Open your files</a> {/if}
            {/each}
          </p>
        {/if}
        {#if !p.Mine || !p.Mine.Live || p.Connectors.some((c) => c.Enabled && c.Key !== 'chat' && !c.Covered)}
          {#if p.Tenant && p.Connectors.some((c) => c.Enabled && c.Key !== 'chat')}
            <form  method="post"  action={"/integrations/" + p.Key + "/connect"}  class="stack">
              {#each p.Connectors as c}
                {#if c.Enabled && c.Key !== 'chat'}
                  <label><input  type="checkbox"  name="connector"  value={c.Key}  checked>{c.Label}</label>
                {/if}
              {/each}
              <div  class="row"><button  type="submit"  class="primary">{p.Mine ? 'Connect again' : 'Connect my ' + p.Label + ' account'}</button></div>
            </form>
            {#if p.Key === 'microsoft' && dot?.Connectors?.Admin}
              <form  method="post"  action="/integrations/microsoft/connect"  class="stack">
                <input  type="hidden"  name="admin"  value="on">
                {#each p.Connectors as c}
                  {#if c.Enabled && c.Key !== 'chat'}<input  type="hidden"  name="connector"  value={c.Key}>{/if}
                {/each}
                <p  class="muted small">
                  Some tenants make everybody ask an administrator before an app can read their data. Granting here is
                  for the whole tenant at once, for the permissions of the connectors that are on.
                </p>
                <div  class="row"><button  type="submit"  class="ghost small">Grant for the whole tenant</button></div>
              </form>
            {/if}
          {:else}
            <p  class="muted small">Nothing is turned on yet, so there is nothing to connect.</p>
          {/if}
        {/if}

        {#if p.Calendars && p.Calendars.length}
          <h3>Calendars</h3>
          <p  class="muted small">
            Your own calendar always syncs. Add another to see it in Hearth, or send it to a department you manage so
            everybody Hearth gives that department can see it.
          </p>
          <ul  class="list">
            {#each p.Calendars as c}
              {#if !c.Primary}
                <li>
                  <div  class="row">
                    <strong>{c.Name}</strong>
                    {#if c.Synced}
                      <span  class="muted small">Synced{c.SpaceID ? ' to a department' : ''}</span>
                      <form  method="post"  action="/settings/connectors/calendar"  class="inline">
                        <input  type="hidden"  name="provider"  value={p.Key}>
                        <input  type="hidden"  name="calendar"  value={c.ID}>
                        <input  type="hidden"  name="action"  value="stop">
                        <button  type="submit"  class="ghost small">Stop</button>
                      </form>
                    {:else}
                      <form  method="post"  action="/settings/connectors/calendar"  class="inline row">
                        <input  type="hidden"  name="provider"  value={p.Key}>
                        <input  type="hidden"  name="calendar"  value={c.ID}>
                        <input  type="hidden"  name="name"  value={c.Name}>
                        <select  name="space"  aria-label={"Where to put " + c.Name}>
                          <option  value="">My own calendar</option>
                          {#each (p.Spaces ?? []) as sp}<option  value={sp.ID}>{sp.Name}</option>{/each}
                        </select>
                        <button  type="submit"  class="small">Sync</button>
                      </form>
                    {/if}
                  </div>
                </li>
              {/if}
            {/each}
          </ul>
        {/if}

        {#each p.Connectors as c}
          {#if c.Key === 'chat' && c.Enabled}
            <h3>{p.Key === 'google' ? 'Google Chat' : 'Teams'} notifications</h3>
            {#if p.ChatHook}
              <p  class="muted small">
                Your notifications are copied to your {p.Key === 'google' ? 'Chat space' : 'Teams chat'}.
                {#if p.ChatHook.LastError}<span  class="err">The last post failed: {p.ChatHook.LastError}</span>{/if}
              </p>
              <form  method="post"  action="/settings/connectors/chat"  class="inline">
                <input  type="hidden"  name="provider"  value={p.Key}>
                <input  type="hidden"  name="action"  value="clear">
                <button  type="submit"  class="ghost small">Stop copying</button>
              </form>
            {:else}
              <form  method="post"  action="/settings/connectors/chat"  class="stack">
                <input  type="hidden"  name="provider"  value={p.Key}>
                <label>Webhook address
                  <input  type="url"  name="webhook"  size="60"  autocomplete="off"
                    placeholder={p.Key === 'google' ? 'https://chat.googleapis.com/v1/spaces/.../messages?key=...' : 'the address the Workflows app gives for a chat'}></label>
                <p  class="muted small">Whoever holds this address can post to your chat, so Hearth stores it encrypted and shows it to nobody.</p>
                <div  class="row"><button  type="submit">Save</button></div>
              </form>
            {/if}
          {/if}
        {/each}

        {#if dot?.Connectors?.Admin && p.People && p.People.length}
          <h3>Who is connected</h3>
          <table  class="list small">
            <thead><tr><th>Person</th><th>Account</th><th>Last runs</th><th></th></tr></thead>
            <tbody>
              {#each p.People as person}
                <tr>
                  <td>{person.Name}</td>
                  <td>{person.Email}{#if person.Revoked} <span  class="err">revoked</span>{/if}</td>
                  <td>
                    {#each (person.Statuses ?? []) as st}
                      <div>{st.Label}: {#if st.Failed}<span  class="err">{st.Error}</span>{:else}{st.Items} items, {F.ago(st.OK)}{/if}</div>
                    {/each}
                  </td>
                  <td>
                    <form  method="post"  action="/settings/connectors/remove"  data-confirm={"Disconnect " + person.Name + "?"}>
                      <input  type="hidden"  name="provider"  value={p.Key}>
                      <input  type="hidden"  name="person"  value={person.PrincipalID}>
                      <button  type="submit"  class="ghost small">Disconnect</button>
                    </form>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      {/if}
    </div>
  {/each}
</section>
{/snippet}
{@render content(dot)}
