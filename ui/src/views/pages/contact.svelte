<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  import { emberopspanel, followupspanel, mailpanel, timeline, addactivity, ownerselect, favstar } from './shared.svelte';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<article  class="card">
  <div  class="row spread">
    <h1>{dot?.Contact?.Name}</h1>
    <div  class="row">
      {@render favstar(dot, "contact", dot?.Contact?.ID)}
      <a  class="chip"  href={("/contacts")}>All contacts</a>
    </div>
  </div>
  {#if F.t(dot?.Contact?.CompanyID)}
    <p  class="muted small">
      <a  href={("/companies/" + S(F.deref_s(dot?.Contact?.CompanyID)))}>{dot?.Contact?.CompanyName}</a>
    </p>
  {/if}

  {#if F.t(dot?.EmberOps)}
  <p  class="row wrap small"  data-emberops>In EmberOps: {#each F.rng(dot?.EmberOps) as [ke, de]}<a  class="chip"  href={S(de?.URL)}  target="_blank"  rel="noopener">{de?.Label}</a>{/each}</p>
  {/if}
  <form  class="fields"  method="post"  action={"/contacts/" + S(dot?.Contact?.ID)}>
    <label  for="name">Name</label>
    <input  id="name"  name="name"  value={S(dot?.Contact?.Name)}  required>
    <label  for="company">Company</label>
    <select  id="company"  name="company">
      <option  value="">No company</option>
      {#each F.rng(dot?.Companies) as [k1, d1]}
        <option  value={S(d1?.ID)}  {...(F.t(F.same(d1?.ID, dot?.Contact?.CompanyID)) ? {"selected": true} : {})}>{d1?.Name}</option>
      {/each}
    </select>
    <label  for="title">Title</label>
    <input  id="title"  name="title"  value={S(dot?.Contact?.Title)}>
    <label  for="email">Email</label>
    <input  id="email"  name="email"  type="email"  value={S(dot?.Contact?.Email)}>
    <label  for="phone">Phone</label>
    <input  id="phone"  name="phone"  value={S(dot?.Contact?.Phone)}>
    <label  for="notes">Notes</label>
    <textarea  id="notes"  name="notes"  rows="3">{dot?.Contact?.Notes}</textarea>
    <div  class="row"><button  type="submit"  class="primary">Save</button></div>
  </form>

  {#if F.t(dot?.SalesOn)}
  <h3>Sales</h3>
  <form  class="fields"  method="post"  action={"/contacts/" + S(dot?.Contact?.ID) + "/sales"}  data-sales-details>
    <label>Owner</label>
    {@render ownerselect(F.dict("Name", "owner", "People", dot?.Owners, "Selected", dot?.Details?.OwnerID))}
    <label  for="linkedin">LinkedIn</label>
    <input  id="linkedin"  name="linkedin"  value={S(dot?.Details?.LinkedIn)}>
    <label  for="extra_emails">Other emails</label>
    <textarea  id="extra_emails"  name="extra_emails"  rows="2"  placeholder="one per line">{F.joinlines(dot?.Details?.ExtraEmails)}</textarea>
    <label  for="extra_phones">Other phones</label>
    <textarea  id="extra_phones"  name="extra_phones"  rows="2"  placeholder="one per line">{F.joinlines(dot?.Details?.ExtraPhones)}</textarea>
    <div  class="row"><button  type="submit"  class="primary">Save sales details</button></div>
  </form>

  {#if F.t(dot?.Details?.MarketingOptOutAt)}
    <p  class="muted small"  data-marketing-optout>Marketing email is off for this person ({dot?.Details?.MarketingOptOutReason}). Only they can turn it back on.</p>
  {/if}

  <h3>Deals</h3>
  {#if F.t(dot?.Deals)}
    <ul  class="files"  data-deals>
      {#each F.rng(dot?.Deals) as [k2, d2]}
        <li><a  href={("/deals/" + S(d2?.ID))}>{d2?.Name}</a> <span  class="tag">{F.stageName(d2?.Stage)}</span>
          <span  class="muted small">{d2?.Amount}</span></li>
      {/each}
    </ul>
  {:else}
    <p  class="muted small">No deals with this person as the contact.</p>
  {/if}

  {@render emberopspanel(dot)}
  {@render followupspanel(dot)}
  {@render mailpanel(dot)}
  {@render timeline(dot)}
  {@render addactivity(F.dict("Type", "contact", "ID", dot?.Contact?.ID))}
  {/if}

  
  <h3>Work and assets</h3>
  {#if F.t(dot?.Links)}
    <ul  class="files"  data-linked>
      {#each F.rng(dot?.Links) as [k3, d3]}
        <li>
          <a  href={("/documents/" + S(d3?.ID))}>{#if F.t(d3?.Title)}{d3?.Title}{:else}Untitled{/if}</a>
          <span  class="tag">{d3?.Kind}</span>
          {#if F.t(d3?.Task)}<span  class={"status " + S(d3?.Task?.Status)}>{F.statusLabel(d3?.Task?.Status)}</span>{/if}
          <form  method="post"  action={"/contacts/" + S(dot?.Contact?.ID) + "/link"}  class="inline">
            <input  type="hidden"  name="target"  value={S(d3?.ID)}>
            <input  type="hidden"  name="unlink"  value="1">
            <button  type="submit"  class="ghost small">Remove</button>
          </form>
        </li>
      {/each}
    </ul>
  {:else}
    <p  class="muted small">Not attached to anything yet.</p>
  {/if}
  <form  method="post"  action={"/contacts/" + S(dot?.Contact?.ID) + "/link"}  class="row">
    <select  name="target"  aria-label="Attach to">
      {#each F.rng(dot?.Assignable) as [k4, d4]}
        <option  value={S(d4?.ID)}>{d4?.Kind} · {#if F.t(d4?.Title)}{d4?.Title}{:else}Untitled{/if}</option>
      {/each}
    </select>
    <button  type="submit">Attach</button>
  </form>

  {#if F.t(dot?.LinkedMail)}
    <h3>Mail</h3>
    <p  class="muted small">Messages from your own mailbox that name this person. Only you see these.</p>
    <ul  class="list"  data-linked-mail>
      {#each dot.LinkedMail as m}
        <li>
          <a  href={m.WebLink}  target="_blank"  rel="noopener">{m.Subject || '(no subject)'}</a>
          <span  class="muted small">{m.From} · {F.date(m.Received)}</span>
          {#if m.Snippet}<div  class="muted small">{m.Snippet}</div>{/if}
        </li>
      {/each}
    </ul>
  {/if}

  <h3>Calls</h3>
  {#if F.t(dot?.Calls)}
    <ul  class="files"  data-calls>
      {#each F.rng(dot?.Calls) as [k5, d5]}
        <li  data-call={S(d5?.ID)}>
          <span  class="tag">{#if F.t(F.eq(d5?.Direction, "in"))}in{:else}out{/if}</span>
          <span  class={"status " + S((F.t(d5?.Answered) ? ("done") : ("blocked")))}>{d5?.Outcome}</span>
          {#if F.t(d5?.DurationS)}<span  class="muted small">{d5?.Minutes}m</span>{/if}
          <span  class="muted small">{F.date(d5?.StartedAt)}</span>
          {#if F.t(d5?.EntityID)}<a  href={("/documents/" + S(F.deref_s(d5?.EntityID)))}>{d5?.EntityTitle}</a>{/if}
          {#if F.t(d5?.Note)}<span  class="muted small">{d5?.Note}</span>{/if}
          <form  method="post"  action={"/calls/" + S(d5?.ID) + "/delete"}  class="inline">
            <input  type="hidden"  name="back"  value={"/contacts/" + S(dot?.Contact?.ID)}>
            <button  type="submit"  class="ghost small">Remove</button>
          </form>
        </li>
      {/each}
    </ul>
  {:else}
    <p  class="muted small">No calls logged with this person.</p>
  {/if}
  <form  method="post"  action="/calls"  class="row wrap">
    <input  type="hidden"  name="contact"  value={S(dot?.Contact?.ID)}>
    <input  type="hidden"  name="back"  value={"/contacts/" + S(dot?.Contact?.ID)}>
    <select  name="direction"  aria-label="Direction">
      <option  value="out">we rang</option>
      <option  value="in">they rang</option>
    </select>
    <select  name="outcome"  aria-label="Outcome">
      {#each F.rng(dot?.CallOutcomes) as [k6, d6]}<option  value={S(d6)}>{d6}</option>{/each}
    </select>
    <input  name="minutes"  type="number"  min="0"  placeholder="mins"  style="width:5rem"             aria-label="Minutes">
    <input  name="at"  type="datetime-local"  aria-label="When">
    <input  name="note"  placeholder="what was said"  aria-label="Note">
    <button  type="submit">Log a call</button>
  </form>

  <form  method="post"  action={"/contacts/" + S(dot?.Contact?.ID) + "/delete"}  class="row">
    <button  type="submit"  class="ghost small">Remove this contact</button>
  </form>
</article>
{/snippet}
{@render content(dot)}
