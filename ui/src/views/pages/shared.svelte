<script lang="ts" module>
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  // Show only one kind of timeline entry. The entries stay on the page, so the filter needs no round trip and is undone by "All".
  function filterTimeline(btn, kind) {
    const box = btn.closest('[data-timeline-box]');
    if (!box) return;
    for (const li of box.querySelectorAll('[data-activity]')) li.hidden = !!kind && li.getAttribute('data-kind') !== kind;
    for (const b of box.querySelectorAll('[data-timeline-filter]')) b.classList.toggle('on', (b.getAttribute('data-timeline-filter') || '') === (kind || ''));
  }
  // An RFC 3339 time as the value a datetime-local field takes, in the reader's own zone.
  function localInput(t) {
    if (!t) return '';
    const d = new Date(t);
    const p = (n) => String(n).padStart(2, '0');
    return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + 'T' + p(d.getHours()) + ':' + p(d.getMinutes());
  }
  export { formspanel, mauticpanel, emberopspanel, bulkbar, followupspanel, mailpanel, orgnode, timeline, addactivity, ownerselect, pager, typechecked, groupselect, grouprow, crmviewsbar, favstar, hoursmins };
</script>
{#snippet orgnode(dot)}
{#if F.t(dot?.Children)}
<details  class="orgnode" {...(F.t(dot?.Open) ? {"open": true} : {})}  data-nav-key={"s-" + S(dot?.ID)}>
  <summary>
    <a  class={"navitem dept" + S((F.t(dot?.Selected) ? (" on") : ""))}  href={("/documents?space=" + S(dot?.ID))}>{dot?.Name}
      {#if F.t(dot?.Documents)}<span  class="ncount">{dot?.Documents}</span>{/if}</a>
    
    <a  class="addfolder"  href={("/spaces?parent=" + S(dot?.ID))}  title={"Add a folder in " + S(dot?.Name)}  aria-label="Add a folder">+</a>
  </summary>
  {#each F.rng(dot?.Children) as [k1, d1]}{@render orgnode(d1)}{/each}
</details>
{:else}
<span  class="navrow">
  <a  class={"navitem dept" + S((F.t(dot?.Selected) ? (" on") : ""))}  href={("/documents?space=" + S(dot?.ID))}>{dot?.Name}
    {#if F.t(dot?.Documents)}<span  class="ncount">{dot?.Documents}</span>{/if}</a>
  <a  class="addfolder"  href={("/spaces?parent=" + S(dot?.ID))}  title={"Add a folder in " + S(dot?.Name)}  aria-label="Add a folder">+</a>
</span>
{/if}
{/snippet}
{#snippet timeline(dot)}
<h3  id="timeline">Timeline</h3>
<div  data-timeline-box>
{#if dot?.Activities?.length > 1}
  <p  class="row wrap"  data-timeline-filters>
    <button  type="button"  class="chip on"  data-timeline-filter=""  onclick={(e) => filterTimeline(e.currentTarget, '')}>All</button>
    {#each ['note', 'call', 'meeting', 'email'] as k}<button  type="button"  class="chip"  data-timeline-filter={k}  onclick={(e) => filterTimeline(e.currentTarget, k)}>{k[0].toUpperCase() + k.slice(1)}s</button>{/each}
  </p>
{/if}
{#if F.t(dot?.Activities)}
  <ul  class="files"  data-timeline>
    {#each F.rng(dot?.Activities) as [k2, d2]}
      <li  data-activity={S(d2?.ID)}  data-kind={d2?.Kind}>
        <span  class="tag">{#if F.t(F.eq(d2?.Kind, "call"))}{#if F.t(F.eq(d2?.Direction, "inbound"))}call in{:else if F.t(F.eq(d2?.Direction, "outbound"))}call out{:else}call{/if}{:else if d2?.Kind === "meeting"}meeting{:else if d2?.Kind === "email"}{#if d2?.Direction === "inbound"}email in{:else}email out{/if}{:else}note{/if}</span>
        <strong>{#if F.t(d2?.Title)}{d2?.Title}{/if}</strong>
        <span  class="muted small">{F.date(d2?.OccurredAt)}{#if F.t(d2?.ByName)} · {d2?.ByName}{/if}</span>
        {#if F.t(d2?.Disposition)}<span  class="status">{d2?.Disposition}</span>{/if}
        {#if F.t(F.dur(d2?.DurationMs))}{@const w3 = F.dur(d2?.DurationMs)}<span  class="muted small">{w3}</span>{/if}
        {#if F.t(d2?.Body)}<div  class="muted small"  style="white-space:pre-wrap">{d2?.Body}</div>{/if}
        <details  class="inline"  data-edit-activity>
          <summary  class="ghost small">Edit</summary>
          <form  method="post"  action={"/activities/" + S(d2?.ID) + "/edit"}  class="fields">
            <input  type="hidden"  name="back"  value={dot?.Path}>
            <label>Title<input  name="title"  value={d2?.Title}></label>
            <label>What happened<textarea  name="body"  rows="3">{d2?.Body}</textarea></label>
            {#if d2?.Kind === "call" || d2?.Kind === "email"}
              <label>Direction<select  name="direction"><option  value=""  selected={!d2?.Direction}>not said</option><option  value="outbound"  selected={d2?.Direction === "outbound"}>we reached out</option><option  value="inbound"  selected={d2?.Direction === "inbound"}>they reached out</option></select></label>
            {/if}
            {#if d2?.Kind === "call"}<label>Outcome<input  name="disposition"  value={d2?.Disposition}></label>{/if}
            <label>When<input  name="when"  type="datetime-local"  value={localInput(d2?.OccurredAt)}></label>
            <div  class="row"><button  type="submit">Save</button></div>
          </form>
        </details>
        <form  method="post"  action={"/activities/" + S(d2?.ID) + "/delete"}  class="inline">
          <button  type="submit"  class="ghost small"  data-confirm="Remove this from every timeline it is on?">Remove</button>
        </form>
      </li>
    {/each}
  </ul>
{:else}
  <p  class="muted small">Nothing written here yet.</p>
{/if}
</div>
{/snippet}
{#snippet addactivity(dot)}
<form  method="post"  action="/activities"  class="row wrap"  data-add-activity>
  <input  type="hidden"  name="target_type"  value={S(dot?.Type)}>
  <input  type="hidden"  name="target_id"  value={S(dot?.ID)}>
  <select  name="kind"  aria-label="Kind"><option  value="note">Note</option><option  value="call">Call</option><option  value="meeting">Meeting (log one that happened)</option><option  value="email">Email (log one that was sent)</option></select>
  <input  name="title"  placeholder="Title"  aria-label="Title">
  <input  name="body"  placeholder="What happened"  style="min-width:14rem"  aria-label="Body">
  <select  name="direction"  aria-label="Direction (calls and emails)"><option  value="">direction</option><option  value="outbound">we reached out</option><option  value="inbound">they reached out</option></select>
  <input  name="disposition"  placeholder="outcome, e.g. Left voicemail"  aria-label="Outcome">
  <input  name="also_company"  placeholder="also about (company names, comma separated)"  style="min-width:14rem"  aria-label="Also about these companies">
  <input  name="minutes"  type="number"  min="0"  step="0.5"  placeholder="mins"  style="width:5rem"  aria-label="Minutes">
  <input  name="when"  type="datetime-local"  aria-label="When">
  <button  type="submit">Add</button>
</form>
{/snippet}
{#snippet ownerselect(dot)}
<select  name={S(dot?.Name)}  aria-label="Owner">
  <option  value="">Nobody</option>
  {#each F.rng(dot?.People) as [k4, d4]}<option  value={S(d4?.PrincipalID)}  {...(F.t(F.eq(d4?.PrincipalID, dot?.Selected)) ? {"selected": true} : {})}>{d4?.DisplayName}</option>{/each}
</select>
{/snippet}
{#snippet pager(dot)}
{#if F.t(dot)}
<p  class="row spread muted small"  data-pager>
  <span>{#if F.t(dot?.Total)}{dot?.From} to {dot?.To} of {dot?.Total}{:else}Nothing matches.{/if}</span>
  <span  class="row">
    {#if F.t(dot?.Prev)}<a  class="chip"  href={(S(dot?.Prev))}>Previous</a>{/if}
    {#if F.t(dot?.Next)}<a  class="chip"  href={(S(dot?.Next))}>Next</a>{/if}
  </span>
</p>
{/if}
{/snippet}
{#snippet groupselect(dot)}
{#if F.t(dot?.CRMGroupOptions)}
<label  class="muted small">Group by
  <select  name="group"  aria-label="Group by">
    <option  value="">Nothing</option>
    {#each F.rng(dot?.CRMGroupOptions) as [kg, dg]}<option  value={S(dg?.Key)}  {...(F.t(F.eq(dg?.Key, dot?.CRMGroup)) ? {"selected": true} : {})}>{dg?.Label}</option>{/each}
  </select></label>
{/if}
{/snippet}
{#snippet grouprow(dot, k, cols)}
{#if F.t(dot?.CRMGroup)}
  {#if k === 0 || dot?.CRMRowGroups?.[k] !== dot?.CRMRowGroups?.[k - 1]}
    <tr  class="grouprow"  data-group={S(dot?.CRMRowGroups?.[k])}>
      <th  colspan={cols}>{dot?.CRMRowGroups?.[k]} <span  class="muted small">{dot?.CRMGroupCounts?.[dot?.CRMRowGroups?.[k]]}</span></th>
    </tr>
  {/if}
{/if}
{/snippet}
{#snippet crmviewsbar(dot)}
{#if F.t(dot?.CRMViewsOn)}
<div  class="viewsbar"  data-crm-views>
  <span  class="muted small">Views</span>
  {#each F.rng(dot?.CRMViews) as [kv, dv]}
    <span  class="viewchip">
      <a  class="chip"  href={('/' + S(dv?.List) + (dv?.Query ? '?' + S(dv?.Query) : ''))}>{dv?.Name}{#if F.t(dv?.Shared) && dv?.OwnerID !== dot?.Me}<span  class="muted"> &middot; {dv?.OwnerName}</span>{/if}</a>
      {#if dv?.OwnerID === dot?.Me}
        <form  class="inline"  method="post"  action={('/crm/views/' + S(dv?.ID) + '/delete')}>
          <input  type="hidden"  name="from"  value={('/' + S(dv?.List))}>
          <button  type="submit"  class="ghost small"  aria-label={('Remove the view ' + S(dv?.Name))}  title="Remove this view">&times;</button>
        </form>
      {/if}
    </span>
  {/each}
  <details  class="saveview">
    <summary  class="chip">Save this view</summary>
    <form  class="row wrap"  method="post"  action="/crm/views">
      <input  type="hidden"  name="from"  value={S(dot?.ListFrom)}>
      <input  name="name"  placeholder="Name this view"  aria-label="View name"  required>
      <label  class="muted small"><input  type="checkbox"  name="shared"  value="1"> Share with everyone</label>
      <button  type="submit"  class="ghost small">Save</button>
    </form>
  </details>
</div>
{/if}
{/snippet}
{#snippet favstar(dot, type, id)}
{#if F.t(dot?.SalesOn)}
<form  class="inline"  method="post"  action="/crm/favorites"  data-favorite={S(dot?.IsFavorite ? 'on' : 'off')}>
  <input  type="hidden"  name="type"  value={S(type)}>
  <input  type="hidden"  name="id"  value={S(id)}>
  <input  type="hidden"  name="on"  value={dot?.IsFavorite ? '0' : '1'}>
  <input  type="hidden"  name="back"  value={('/' + (type === 'company' ? 'companies' : type === 'contact' ? 'contacts' : 'deals') + '/' + S(id))}>
  <button  type="submit"  class={"chip" + (dot?.IsFavorite ? " on" : "")}  aria-pressed={dot?.IsFavorite ? 'true' : 'false'}  title={dot?.IsFavorite ? 'Remove from your favorites' : 'Add to your favorites'}>{dot?.IsFavorite ? '\u2605 Favorite' : '\u2606 Favorite'}</button>
</form>
{/if}
{/snippet}
{#snippet hoursmins(hName, mName, h, m, err, errId)}
<span  class="hm"  role="group"  aria-label="How long">
  <label  class="hmfield"><input  name={hName}  type="number"  inputmode="numeric"  pattern="[0-9]*"  min="0"  max="999"  step="1"  placeholder="0"  value={S(h)}  aria-label="Hours"  aria-invalid={err ? 'true' : undefined}  aria-describedby={err ? errId : undefined}><span>hours</span></label>
  <label  class="hmfield"><input  name={mName}  type="number"  inputmode="numeric"  pattern="[0-9]*"  min="0"  max="59"  step="1"  placeholder="0"  value={S(m)}  aria-label="Minutes"  aria-invalid={err ? 'true' : undefined}  aria-describedby={err ? errId : undefined}><span>minutes</span></label>
</span>
{#if err}<p  id={errId}  class="error"  role="alert">{err}</p>{/if}
{/snippet}
{#snippet typechecked(dot)}{@const v_k = dot?.Key}{#if F.t(dot?.Labels)}{#each F.rng(dot?.Labels?.Types) as [k5, d5]}{#if F.t(F.eq(d5?.Key, v_k))}checked{/if}{/each}{/if}{/snippet}

{#snippet followupspanel(dot)}
{#if dot?.Desk}
<h3  id="followups">Follow-ups</h3>
{#if dot.Desk.Followups?.length}
  <ul  class="files"  data-followups>
    {#each dot.Desk.Followups as f (f.ID)}
      <li  data-followup={f.ID}>
        <span  class={"tag" + (f.Overdue ? " overdue" : "")}>{f.Status === "done" ? "done" : f.DueAt ? "due " + f.DueAt.slice(0, 10) : "no date"}</span>
        <strong>{f.Title}</strong>
        <span  class="muted small">{f.AssigneeName}</span>
        {#if f.Body}<div  class="muted small"  style="white-space:pre-wrap">{f.Body}</div>{/if}
        <form  method="post"  action={"/crm/followups/" + f.ID + "/done"}  class="inline">
          <input  type="hidden"  name="done"  value={f.Status === "done" ? "0" : "1"}>
          <input  type="hidden"  name="back"  value={dot.Path + "#followups"}>
          <button  type="submit"  class="ghost small">{f.Status === "done" ? "Open again" : "Done"}</button>
        </form>
        <form  method="post"  action={"/crm/followups/" + f.ID + "/delete"}  class="inline">
          <input  type="hidden"  name="back"  value={dot.Path + "#followups"}>
          <button  type="submit"  class="ghost small"  data-confirm="Remove this follow-up?">Remove</button>
        </form>
      </li>
    {/each}
  </ul>
{:else}
  <p  class="muted small">Nothing owed here.</p>
{/if}
<form  method="post"  action="/crm/followups"  class="row wrap"  data-add-followup>
  <input  type="hidden"  name="target_type"  value={dot.FavoriteType || (dot.Deal ? "deal" : dot.Contact ? "contact" : "company")}>
  <input  type="hidden"  name="target_id"  value={dot.Deal?.ID || dot.Contact?.ID || dot.Company?.ID}>
  <input  name="title"  placeholder="Follow up on"  style="min-width:14rem"  aria-label="Follow-up"  required>
  <input  name="due"  type="date"  aria-label="Due"  value={dot.Desk.Today}>
  <select  name="assignee"  aria-label="Assigned to">
    <option  value="me">Me</option>
    {#each dot.Owners || [] as o}<option  value={o.PrincipalID}>{o.DisplayName}</option>{/each}
  </select>
  <button  type="submit">Add follow-up</button>
</form>
{/if}
{/snippet}
{#snippet mailpanel(dot)}
{#if dot?.Desk?.MailOn}
<h3  id="mail-out">Email and meetings</h3>
<p  class="muted small">Sent and booked from your own connected account, and logged on the timeline.</p>
<form  method="post"  action="/crm/email"  class="fields"  data-send-email>
  <input  type="hidden"  name="target_type"  value={dot.Deal ? "deal" : dot.Contact ? "contact" : "company"}>
  <input  type="hidden"  name="target_id"  value={dot.Deal?.ID || dot.Contact?.ID || dot.Company?.ID}>
  <label  for="mail-provider">From</label>
  <select  id="mail-provider"  name="provider"><option  value="google">Google</option><option  value="microsoft">Microsoft</option></select>
  <label  for="mail-to">To</label>
  <input  id="mail-to"  name="to"  value={dot.Contact?.Email || ""}  placeholder="name@example.com"  required>
  <label  for="mail-subject">Subject</label>
  <input  id="mail-subject"  name="subject"  required>
  <label  for="mail-body">Message</label>
  <textarea  id="mail-body"  name="body"  rows="5"  required></textarea>
  <div  class="row"><button  type="submit">Send email</button></div>
</form>
<form  method="post"  action="/crm/meeting"  class="fields"  data-book-meeting>
  <input  type="hidden"  name="target_type"  value={dot.Deal ? "deal" : dot.Contact ? "contact" : "company"}>
  <input  type="hidden"  name="target_id"  value={dot.Deal?.ID || dot.Contact?.ID || dot.Company?.ID}>
  <label  for="mt-provider">Calendar</label>
  <select  id="mt-provider"  name="provider"><option  value="google">Google</option><option  value="microsoft">Microsoft</option></select>
  <label  for="mt-title">Meeting</label>
  <input  id="mt-title"  name="title"  required>
  <label  for="mt-when">Starts</label>
  <input  id="mt-when"  name="when"  type="datetime-local"  required>
  <label  for="mt-mins">Minutes</label>
  <input  id="mt-mins"  name="minutes"  type="number"  min="5"  step="5"  value="30">
  <label  for="mt-invite">Invite</label>
  <input  id="mt-invite"  name="invite"  value={dot.Contact?.Email || ""}  placeholder="emails, comma separated">
  <label  for="mt-notes">Notes</label>
  <textarea  id="mt-notes"  name="notes"  rows="2"></textarea>
  <div  class="row"><button  type="submit">Book meeting</button></div>
</form>
{/if}
{/snippet}

{#snippet bulkbar(dot, type)}
{#if dot?.SalesNav}
<form  id="bulkform"  method="post"  action="/crm/bulk"  class="row wrap"  data-bulk>
  <input  type="hidden"  name="type"  value={type}>
  <input  type="hidden"  name="back"  value={dot?.Path}>
  <span  class="muted small">With the ticked rows:</span>
  <select  name="owner"  aria-label="Set owner">
    <option  value="">Owner: no change</option>
    <option  value="me">Owner: me</option>
    <option  value="none">Owner: nobody</option>
    {#each dot?.Owners || [] as o}<option  value={o.PrincipalID}>Owner: {o.DisplayName}</option>{/each}
  </select>
  {#if type === "deal"}
    <select  name="stage"  aria-label="Move to stage">
      <option  value="">Stage: no change</option>
      {#each dot?.DealStages || [] as st}<option  value={st}>Stage: {F.stageName(st)}</option>{/each}
    </select>
  {/if}
  {#if type === "company"}<input  name="add_tag"  placeholder="add a tag"  aria-label="Add a tag">{/if}
  <button  type="submit"  class="ghost small">Apply</button>
</form>
{/if}
{/snippet}

{#snippet emberopspanel(dot)}
{#if dot?.Desk?.EmberOps}
<h3  id="emberops">In EmberOps</h3>
{#if dot.Desk.EmberOps.Quotes?.length}
  <table  class="list"  data-emberops-quotes>
    <thead><tr><th>Quote</th><th>For</th><th>Status</th><th>Total</th><th>Valid till</th></tr></thead>
    <tbody>
      {#each dot.Desk.EmberOps.Quotes as q (q.Name)}
        <tr><td><a  href={q.URL}  target="_blank"  rel="noopener">{q.Name}</a></td><td>{q.Party}</td><td><span  class="tag">{q.Status}</span></td>
          <td class="muted small">{q.Total.toLocaleString(undefined, { style: 'currency', currency: q.Currency || 'USD' })}</td><td class="muted small">{q.ValidTill}</td></tr>
      {/each}
    </tbody>
  </table>
{:else}
  <p  class="muted small">No quotes yet.</p>
{/if}
{#if dot.Desk.EmberOps.NewQuoteURL}<p><a  class="chip"  href={dot.Desk.EmberOps.NewQuoteURL}  target="_blank"  rel="noopener"  data-new-quote>New quote in EmberOps</a></p>{/if}
{#if dot.Desk.EmberOps.Calls?.length}
  <h4>Calls EmberOps logged</h4>
  <ul  class="files"  data-emberops-calls>
    {#each dot.Desk.EmberOps.Calls as c (c.Name)}
      <li><span  class="tag">{c.Type}</span> <a  href={c.URL}  target="_blank"  rel="noopener">{c.Start}</a> <span  class="muted small">{c.From} to {c.To}{c.Seconds ? ', ' + Math.ceil(c.Seconds / 60) + ' min' : ''}{c.Status ? ', ' + c.Status : ''}</span></li>
    {/each}
  </ul>
{/if}
{/if}
{/snippet}

{#snippet mauticpanel(dot)}
{#if dot?.Desk?.Mautic || dot?.Desk?.MauticNote}
<h3  id="mautic">In Mautic</h3>
{#if dot.Desk.MauticNote}<p  class="muted small">{dot.Desk.MauticNote}</p>{/if}
{#if dot.Desk.Mautic}
  {#if dot.Desk.Mautic.OptedOut}<p  class="muted small"  data-mautic-optout>Marketing email is off for this person. Mautic will not send to them.</p>{/if}
  <p  class="muted small">What Mautic recorded for them (sent, opened, clicked, forms, unsubscribed) is on the timeline.</p>
  <ul  class="files"  data-mautic-segments>
    {#each dot.Desk.Mautic.Segments || [] as sg (sg.ID)}
      <li>
        <span  class={'tag' + (sg.Member ? ' on' : '')}>{sg.Member ? 'in' : 'not in'}</span> {sg.Name}
        {#if sg.Locked}<span  class="muted small">kept by the sync</span>
        {:else}
          <form  method="post"  action="/crm/mautic/segment"  class="inline">
            <input  type="hidden"  name="contact_id"  value={dot.Contact?.ID} />
            <input  type="hidden"  name="id"  value={sg.ID} />
            <input  type="hidden"  name="on"  value={sg.Member ? '0' : '1'} />
            <button  type="submit"  class="ghost small">{sg.Member ? 'Remove' : 'Add'}</button>
          </form>
        {/if}
      </li>
    {/each}
  </ul>
  {#if dot.Desk.Mautic.Campaigns?.length}
    <h4>Campaigns</h4>
    <ul  class="files"  data-mautic-campaigns>
      {#each dot.Desk.Mautic.Campaigns as c (c.ID)}
        <li>
          <span  class={'tag' + (c.Member ? ' on' : '')}>{c.Member ? 'in' : 'not in'}</span> {c.Name}
          <form  method="post"  action="/crm/mautic/campaign"  class="inline">
            <input  type="hidden"  name="contact_id"  value={dot.Contact?.ID} />
            <input  type="hidden"  name="id"  value={c.ID} />
            <input  type="hidden"  name="on"  value={c.Member ? '0' : '1'} />
            <button  type="submit"  class="ghost small">{c.Member ? 'Remove' : 'Add'}</button>
          </form>
        </li>
      {/each}
    </ul>
  {/if}
{/if}
{/if}
{/snippet}

{#snippet formspanel(dot)}
{#if dot?.Desk?.Forms?.length}
<h3  id="forms">Form responses</h3>
<ul  class="files"  data-form-responses>
  {#each dot.Desk.Forms as e}
    <li>
      <strong>{e.Form}</strong> <span  class="muted small">{(e.Created || '').slice(0, 10)}{e.Finished ? '' : ', not finished'}</span>
      {#each e.Answers || [] as a}<div  class="muted small">{a.Question}: {a.Answer}</div>{/each}
    </li>
  {/each}
</ul>
{/if}
{/snippet}
