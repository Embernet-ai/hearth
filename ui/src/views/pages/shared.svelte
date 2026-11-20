<script lang="ts" module>
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  export { orgnode, timeline, addactivity, ownerselect, pager, typechecked, groupselect, grouprow, crmviewsbar, favstar, hoursmins };
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
{#if F.t(dot?.Activities)}
  <ul  class="files"  data-timeline>
    {#each F.rng(dot?.Activities) as [k2, d2]}
      <li  data-activity={S(d2?.ID)}>
        <span  class="tag">{#if F.t(F.eq(d2?.Kind, "call"))}{#if F.t(F.eq(d2?.Direction, "inbound"))}call in{:else if F.t(F.eq(d2?.Direction, "outbound"))}call out{:else}call{/if}{:else}note{/if}</span>
        <strong>{#if F.t(d2?.Title)}{d2?.Title}{/if}</strong>
        <span  class="muted small">{F.date(d2?.OccurredAt)}{#if F.t(d2?.ByName)} · {d2?.ByName}{/if}</span>
        {#if F.t(d2?.Disposition)}<span  class="status">{d2?.Disposition}</span>{/if}
        {#if F.t(F.dur(d2?.DurationMs))}{@const w3 = F.dur(d2?.DurationMs)}<span  class="muted small">{w3}</span>{/if}
        {#if F.t(d2?.Body)}<div  class="muted small"  style="white-space:pre-wrap">{d2?.Body}</div>{/if}
        <form  method="post"  action={"/activities/" + S(d2?.ID) + "/delete"}  class="inline">
          <button  type="submit"  class="ghost small"  data-confirm="Remove this from every timeline it is on?">Remove</button>
        </form>
      </li>
    {/each}
  </ul>
{:else}
  <p  class="muted small">Nothing written here yet.</p>
{/if}
{/snippet}
{#snippet addactivity(dot)}
<form  method="post"  action="/activities"  class="row wrap"  data-add-activity>
  <input  type="hidden"  name="target_type"  value={S(dot?.Type)}>
  <input  type="hidden"  name="target_id"  value={S(dot?.ID)}>
  <select  name="kind"  aria-label="Kind"><option  value="note">Note</option><option  value="call">Call</option></select>
  <input  name="title"  placeholder="Title"  aria-label="Title">
  <input  name="body"  placeholder="What happened"  style="min-width:14rem"  aria-label="Body">
  <select  name="direction"  aria-label="Direction (calls)"><option  value="">direction</option><option  value="outbound">we rang</option><option  value="inbound">they rang</option></select>
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
