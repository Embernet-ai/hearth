<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Calendar</h1>
  <p  class="muted small">
    Your own events, your department's, and the work assigned to you that falls
    due. Due dates are read from the work orders themselves rather than copied
    here, so they cannot drift apart from them.
  </p>

  <div  class="row spread">
    <div  class="row">
      <a  class="chip"  href={("/calendar?month=" + S(dot?.PrevMonth))}>Previous</a>
      <span  class="muted">{F.month(dot?.Month)}</span>
      <a  class="chip"  href={("/calendar?month=" + S(dot?.NextMonth))}>Next</a>
    </div>
  </div>

  
  <table  class="list calendar">
    <thead>
      <tr>
        <th>Mon</th><th>Tue</th><th>Wed</th><th>Thu</th>
        <th>Fri</th><th>Sat</th><th>Sun</th>
      </tr>
    </thead>
    <tbody>
      {#each F.rng(dot?.Days) as [k1, d1]}
        <tr>
          {#each F.rng(d1) as [k2, d2]}
            <td  class={"calcell" + S((F.t(F.not(d2?.InMonth)) ? (" muted") : "")) + S((F.t(d2?.Today) ? (" on") : ""))}>
              <div  class="small">{F.day(d2?.Date)}</div>
              {#each F.rng(d2?.Entries) as [k3, d3]}
                {#if F.t(d3?.DocumentID)}
                  <a  class="tag"  href={("/documents/" + S(d3?.DocumentID))}>{d3?.Title}</a>
                {:else if F.t(d3?.Link)}
                  
                  <a  class="tag"  href={(S(d3?.Link))}  title="from Google">{d3?.Title}</a>
                {:else}
                  <div  class="tag"  title={S(d3?.Notes)}>{d3?.Title}</div>
                  {#if F.t(d3?.ID) && S(d3?.Kind) === "event" && !S(d3?.Title).startsWith("Out: ")}
                    <form  method="post"  action={"/calendar/" + S(d3?.ID) + "/delete"}  data-confirm="Delete this event?"  data-event-delete={S(d3?.ID)}><button  class="ghost small">Delete</button></form>
                  {/if}
                {/if}
              {/each}
            </td>
          {/each}
        </tr>
      {/each}
    </tbody>
  </table>

  <h3>Add an event</h3>
  <form  method="post"  action="/calendar"  class="stack card">
    <div  class="row">
      <input  name="title"  required placeholder="What it is"  aria-label="Title"  style="flex:1">
      <input  name="location"  placeholder="Where"  aria-label="Where"  style="width:12rem">
    </div>
    <div  class="row">
      <label  for="starts">From</label>
      <input  id="starts"  name="starts"  type="datetime-local"  required aria-label="From">
      <label  for="ends">To</label>
      <input  id="ends"  name="ends"  type="datetime-local"  aria-label="To">
      <label  for="all_day">All day</label>
      <input  id="all_day"  name="all_day"  type="checkbox"  value="1">
    </div>
    <p  class="muted small">
      For an all-day event, tick the box and give the dates only. A day is a
      date rather than a time, which is what keeps it on the right day for
      somebody reading it in another timezone.
    </p>
    <div  class="row">
      <select  name="department"  aria-label="Whose">
        <option  value="">Mine</option>
        {#each F.rng(dot?.Departments) as [k4, d4]}<option  value={S(d4?.ID)}>{d4?.Name}</option>{/each}
      </select>
      <input  name="notes"  placeholder="Notes"  aria-label="Notes"  style="flex:1">
      <button  type="submit">Add</button>
    </div>
  </form>

  <h3>On your phone</h3>
  {#if F.t(dot?.FeedToken)}
    
    <p  class="err">
      This is the only time this address is shown. Copy it now. Subscribing to
      it in a calendar application gives that application read access to
      everything on this page, so treat it like a password.
    </p>
    <p><code>{dot?.BaseURL}/calendar/feed/{dot?.FeedToken}.ics</code></p>
  {/if}

  <p  class="muted small">
    A subscription is read only and covers only what you can already see. If an
    address is ever shared by accident, replace it: the previous one stops
    working immediately.
  </p>
  <form  method="post"  action="/calendar/feed"  class="row">
    {#if F.t(dot?.HasFeed)}
      <span  class="muted small">A feed has existed since {F.date(dot?.FeedMadeAt)}.</span>
      <button  type="submit"  name="action"  value="issue"  class="ghost small">Replace the address</button>
      <button  type="submit"  name="action"  value="revoke"  class="ghost small">Turn it off</button>
    {:else}
      <button  type="submit"  name="action"  value="issue">Create a subscribable address</button>
    {/if}
  </form>
</section>
{/snippet}
{@render content(dot)}
