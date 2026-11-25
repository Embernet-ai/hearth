<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
  const m = $derived(dot?.Marketing);
  const when = (iso) => (iso ? iso.slice(0, 16) : '');
  const state = (e) => (!e.Published ? 'Draft' : e.PublishUp && new Date(e.PublishUp) > new Date() ? 'Scheduled' : 'Live');
</script>

{#snippet content(dot: any)}
<section>
  <h1>Marketing</h1>
  {#if F.t(m?.Notice)}<p  class="card small"  data-flash>{m?.Notice}</p>{/if}

  {#if !m?.Configured}
    <p  class="muted"  data-not-set-up>
      Marketing is not connected on this installation. Set <code>HEARTH_MARKETING_URL</code>,
      <code>HEARTH_MARKETING_CLIENT_ID</code> and <code>HEARTH_MARKETING_CLIENT_SECRET</code> to the address and API client of your
      email marketing app, and emails, campaigns and segments appear here.
    </p>
  {:else}
    <nav  class="row tabs">
      <a  class={'chip' + (m.Tab === 'emails' ? ' on' : '')}  href="/marketing?tab=emails">Emails</a>
      <a  class={'chip' + (m.Tab === 'campaigns' ? ' on' : '')}  href="/marketing?tab=campaigns">Campaigns</a>
      <a  class={'chip' + (m.Tab === 'segments' ? ' on' : '')}  href="/marketing?tab=segments">Segments</a>
      <a  class={'chip' + (m.Tab === 'contacts' ? ' on' : '')}  href="/marketing?tab=contacts">Contacts</a>
    </nav>

    {#if m.Tab === 'emails'}
      {#if m.New || m.Email}
        {@const e = m.Email}
        <form  method="post"  action="/marketing/email"  class="card stack"  data-email-form>
          <h2>{e ? 'Edit email' : 'New email'}</h2>
          {#if e}<input  type="hidden"  name="id"  value={e.ID}>{/if}
          <label>Name <input  name="name"  value={e?.Name ?? ''}  required></label>
          <label>Subject <input  name="subject"  value={e?.Subject ?? ''}  required></label>
          <label>Kind
            <select  name="type"><option  value="list"  selected={(e?.Type ?? 'list') === 'list'}>Sent to segments</option><option  value="template"  selected={e?.Type === 'template'}>Used by a campaign</option></select>
          </label>
          <label>Segments
            <select  name="segment"  multiple>{#each (m.Segments ?? []) as s}<option  value={s.ID}  selected={(e?.Segments ?? []).includes(s.ID)}>{s.Name}</option>{/each}</select>
          </label>
          <label>Message (HTML) <textarea  name="html"  rows="10">{e?.HTML ?? ''}</textarea></label>
          <label>Go live at <input  type="datetime-local"  name="publish_up"  value={when(e?.PublishUp)}></label>
          <label  class="small"><input  type="checkbox"  name="published"  checked={e?.Published ?? false}> Published (an email with a date above goes live then)</label>
          <div  class="row"><button  type="submit"  class="primary">Save</button><a  class="ghost small"  href="/marketing?tab=emails">Cancel</a></div>
        </form>
        {#if e}
          <form  method="post"  action={"/marketing/email/" + e.ID + "/test"}  class="card row"  data-test-send>
            <label  class="grow">Send a test of this email to <input  type="email"  name="to"  placeholder="you@example.com"  required></label>
            <button  type="submit">Send test</button>
          </form>
        {/if}
      {:else}
        <p><a  class="primary small"  href="/marketing?tab=emails&new=1">New email</a></p>
        <form  method="get"  action="/marketing"  class="row"><input  type="hidden"  name="tab"  value="emails"><input  type="search"  name="q"  value={m.Search}  placeholder="Search emails"><button  class="ghost small">Search</button></form>
        {#if m.Emails && m.Emails.length}
          <table  class="list small"  data-emails>
            <thead><tr><th>Email</th><th>State</th><th>Sent</th><th>Opened</th><th></th></tr></thead>
            <tbody>
              {#each m.Emails as e}
                <tr  data-email={e.ID}>
                  <td><a  href={"/marketing?tab=emails&edit=" + e.ID}>{e.Name}</a><div  class="muted">{e.Subject}</div></td>
                  <td>{state(e)}{#if e.Published && e.PublishUp} <span  class="muted">{e.PublishUp.slice(0, 16)}</span>{/if}</td>
                  <td>{e.Sent}</td><td>{e.Read}</td>
                  <td  class="row">
                    <form  method="post"  action={"/marketing/email/" + e.ID + "/publish"}  class="inline">
                      {#if !e.Published}<input  type="hidden"  name="on"  value="on">{/if}
                      <button  class="ghost small">{e.Published ? 'Unpublish' : 'Publish'}</button>
                    </form>
                    {#if e.Type === 'list' && e.Published}
                      <form  method="post"  action={"/marketing/email/" + e.ID + "/send"}  class="inline"  data-confirm="Send this email to its segments now?"><button  class="small">Send now</button></form>
                    {/if}
                    <form  method="post"  action={"/marketing/email/" + e.ID + "/delete"}  class="inline"  data-confirm="Delete this email?"><button  class="ghost small">Delete</button></form>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {:else}<p  class="muted">No emails yet.</p>{/if}
      {/if}
    {/if}

    {#if m.Tab === 'campaigns'}
      {#if m.New || m.Campaign}
        {@const c = m.Campaign}
        <form  method="post"  action="/marketing/campaign"  class="card stack"  data-campaign-form>
          <h2>{c ? 'Edit campaign' : 'New campaign'}</h2>
          {#if c}<input  type="hidden"  name="id"  value={c.ID}>{/if}
          <label>Name <input  name="name"  value={c?.Name ?? ''}  required></label>
          <label>About <input  name="description"  value={c?.Description ?? ''}></label>
          <label>Segments
            <select  name="segment"  multiple>{#each (m.Segments ?? []) as s}<option  value={s.ID}  selected={(c?.Segments ?? []).includes(s.ID)}>{s.Name}</option>{/each}</select>
          </label>
          {#if !c}
            <label>Email it sends
              <select  name="email">{#each (m.Emails ?? []) as e}<option  value={e.ID}>{e.Name}</option>{/each}</select>
            </label>
            <label>Wait this many days after somebody joins <input  type="number"  name="delay_days"  min="0"  value="0"></label>
          {/if}
          <label>Starts <input  type="datetime-local"  name="publish_up"  value={when(c?.PublishUp)}></label>
          <label>Ends <input  type="datetime-local"  name="publish_down"  value={when(c?.PublishDown)}></label>
          <label  class="small"><input  type="checkbox"  name="published"  checked={c?.Published ?? false}> Published</label>
          {#if c}<p  class="muted small">{c.Contacts} people are in this campaign.</p>{/if}
          <div  class="row"><button  type="submit"  class="primary">Save</button><a  class="ghost small"  href="/marketing?tab=campaigns">Cancel</a></div>
        </form>
      {:else}
        <p><a  class="primary small"  href="/marketing?tab=campaigns&new=1">New campaign</a></p>
        {#if m.Campaigns && m.Campaigns.length}
          <table  class="list small"  data-campaigns>
            <thead><tr><th>Campaign</th><th>State</th><th>People</th><th></th></tr></thead>
            <tbody>
              {#each m.Campaigns as c}
                <tr  data-campaign={c.ID}>
                  <td><a  href={"/marketing?tab=campaigns&edit=" + c.ID}>{c.Name}</a><div  class="muted">{c.Description}</div></td>
                  <td>{c.Published ? 'Published' : 'Draft'}</td><td>{c.Contacts}</td>
                  <td  class="row">
                    <form  method="post"  action={"/marketing/campaign/" + c.ID + "/publish"}  class="inline">{#if !c.Published}<input  type="hidden"  name="on"  value="on">{/if}<button  class="ghost small">{c.Published ? 'Unpublish' : 'Publish'}</button></form>
                    <form  method="post"  action={"/marketing/campaign/" + c.ID + "/delete"}  class="inline"  data-confirm="Delete this campaign?"><button  class="ghost small">Delete</button></form>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {:else}<p  class="muted">No campaigns yet.</p>{/if}
      {/if}
    {/if}

    {#if m.Tab === 'contacts'}
      {#if m.New || m.Contact}
        {@const c = m.Contact}
        <form  method="post"  action="/marketing/contact"  class="card stack"  data-contact-form>
          <h2>{c ? c.First + ' ' + c.Last : 'New contact'}</h2>
          {#if c}<input  type="hidden"  name="id"  value={c.ID}>{/if}
          <div  class="row"><label>First name <input  name="first"  value={c?.First ?? ''}></label><label>Last name <input  name="last"  value={c?.Last ?? ''}></label></div>
          <label>Email <input  type="email"  name="email"  value={c?.Email ?? ''}  required></label>
          <label>Company <input  name="company"  value={c?.Company ?? ''}></label>
          <label>Tags, separated by commas <input  name="tags"  value={(c?.Tags ?? []).join(', ')}></label>
          <div  class="row"><button  type="submit"  class="primary">Save</button><a  class="ghost small"  href="/marketing?tab=contacts">Cancel</a></div>
        </form>
        {#if c}
          <div  class="card stack"  data-contact-actions>
            <h3>Segments</h3>
            <form  method="post"  action={"/marketing/contact/" + c.ID + "/segment"}  class="row">
              <select  name="segment"  aria-label="Segment">{#each (m.Segments ?? []) as s}<option  value={s.ID}>{s.Name}</option>{/each}</select>
              <button  name="do"  value="add"  class="small">Add to segment</button><button  name="do"  value="remove"  class="ghost small">Take out of segment</button>
            </form>
            <h3>Send an email to this person now</h3>
            <form  method="post"  action={"/marketing/contact/" + c.ID + "/send"}  class="row"  data-confirm="Send this email to this person now?">
              <select  name="email"  aria-label="Email">{#each (m.Emails ?? []) as e}<option  value={e.ID}>{e.Name}</option>{/each}</select>
              <button  class="small">Send</button>
            </form>
            <h3>Email permission</h3>
            <form  method="post"  action={"/marketing/contact/" + c.ID + "/dnc"}  class="row">
              {#if c.DoNotEmail}<span  class="muted small">Do not email is on.</span><button  name="on"  value="0"  class="small">Allow email again</button>
              {:else}<button  name="on"  value="1"  class="ghost small">Mark do not email</button>{/if}
            </form>
            <form  method="post"  action={"/marketing/contact/" + c.ID + "/delete"}  class="row"  data-confirm="Delete this contact from the email marketing app?"><button  class="ghost small">Delete contact</button></form>
          </div>
        {/if}
      {:else}
        <p><a  class="primary small"  href="/marketing?tab=contacts&new=1">New contact</a></p>
        <form  method="get"  action="/marketing"  class="row"><input  type="hidden"  name="tab"  value="contacts"><input  type="search"  name="q"  value={m.Search}  placeholder="Search a name, an address or tag:vip"><button  class="ghost small">Search</button></form>
        {#if m.Contacts && m.Contacts.length}
          <table  class="list small"  data-contacts>
            <thead><tr><th>Person</th><th>Company</th><th>Tags</th><th>Last active</th><th></th></tr></thead>
            <tbody>
              {#each m.Contacts as c}
                <tr  data-contact={c.ID}>
                  <td><a  href={"/marketing?tab=contacts&edit=" + c.ID}>{(c.First + ' ' + c.Last).trim() || c.Email}</a><div  class="muted">{c.Email}</div></td>
                  <td>{c.Company}</td><td>{(c.Tags ?? []).join(', ')}</td><td>{c.LastActive ? c.LastActive.slice(0, 10) : ''}</td>
                  <td>{#if c.DoNotEmail}<span  class="tag">do not email</span>{/if}</td>
                </tr>
              {/each}
            </tbody>
          </table>
          <p  class="muted small">{m.Total} contacts</p>
        {:else}<p  class="muted">No contacts match.</p>{/if}
      {/if}
    {/if}

    {#if m.Tab === 'segments'}
      <form  method="post"  action="/marketing/segment"  class="card stack"  data-segment-form>
        <h2>{m.Segment ? 'Edit segment' : 'New segment'}</h2>
        {#if m.Segment}<input  type="hidden"  name="id"  value={m.Segment.ID}>{/if}
        <label>Name <input  name="name"  value={m.Segment?.Name ?? ''}  required></label>
        <label>About <input  name="description"  value={m.Segment?.Description ?? ''}></label>
        <label  class="small"><input  type="checkbox"  name="published"  checked={m.Segment?.Published ?? true}> Published</label>
        <div  class="row"><button  type="submit"  class="primary">Save</button>{#if m.Segment}<a  class="ghost small"  href="/marketing?tab=segments">Cancel</a>{/if}</div>
      </form>
      {#if m.Segments && m.Segments.length}
        <table  class="list small"  data-segments>
          <thead><tr><th>Segment</th><th>People</th><th></th></tr></thead>
          <tbody>
            {#each m.Segments as s}
              <tr  data-segment={s.ID}>
                <td><a  href={"/marketing?tab=segments&edit=" + s.ID}>{s.Name}</a><div  class="muted">{s.Description}</div></td><td>{s.Contacts}</td>
                <td><form  method="post"  action={"/marketing/segment/" + s.ID + "/delete"}  class="inline"  data-confirm="Delete this segment?"><button  class="ghost small">Delete</button></form></td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    {/if}
  {/if}
</section>
{/snippet}
{@render content(dot)}
