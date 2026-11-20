<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  import { hoursmins } from './shared.svelte';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
  // A log-time form that was refused comes back inside the document, with the reason beside the field. The form lives in a
  // popover that a redraw closes, so open it again and put the cursor on the field the reason is about.
  $effect(() => {
    if (!pg?.TimeForm?.Error) return;
    queueMicrotask(() => {
      const f = document.querySelector('form[data-time-form]');
      try { f?.closest('[popover]')?.showPopover?.(); } catch { /* already open */ }
      (f?.querySelector('input[name=hours]') as HTMLInputElement | null)?.focus();
    });
  });
</script>
{#snippet peek(dot)}
<article  class="card peek">
  <div  class="row spread"  data-drag-handle title="Drag to move">
    <h1>{#if F.t(dot?.Doc?.Title)}{dot?.Doc?.Title}{:else}Untitled{/if}</h1>
    <div  class="row">
      <span  class="tag">{dot?.Doc?.Kind}</span>
      <a  class="chip"  href={("/documents/" + S(dot?.Doc?.ID))}>Open full page</a>
      <button  type="button"  class="chip"  popovertarget="taskpanel">Close</button>
    </div>
  </div>

  {@render fieldsrow(dot)}

  <details  class="fold"  open>
    <summary><h3>Details</h3></summary>
    {#if F.t(dot?.BodyHTML)}
      <div  class="prose">{@html dot?.BodyHTML}</div>
    {:else}
      <p  class="muted small">No details yet.</p>
    {/if}
  </details>

  {#if F.t(dot?.Checklist)}
    <h3>Checklist <span  class="muted small">{dot?.Progress?.Done} of {dot?.Progress?.Total}</span></h3>
    <ul  class="checklist">
      {#each F.rng(dot?.Checklist) as [k1, d1]}
        <li  class={S((F.t(d1?.Done) ? ("ticked") : ""))}>
          <div  class="row">
            <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/checklist/" + S(d1?.ID)}  class="inline">
              <input  type="hidden"  name="done"  value={S((F.t(d1?.Done) ? ("0") : ("1")))}>
              <input  type="hidden"  name="peek"  value="1">
              <button  type="submit"  class={"tick" + S((F.t(d1?.Done) ? (" on") : ""))}  aria-label={S((F.t(d1?.Done) ? ("Mark not done") : ("Mark done")))}>{#if F.t(d1?.Done)}&#10003;{/if}</button>
            </form>
            <span  class="text">{d1?.Text}</span>
            {#if F.t(d1?.Done)}<span  class="muted small">{d1?.DoneName} · {F.ago(d1?.DoneAt)}</span>{/if}
          </div>
        </li>
      {/each}
    </ul>
  {/if}

  {#if F.t(dot?.Attachments)}
    <details  class="fold"  open>
      <summary><h3>Attachments <span  class="muted small">{F.len(dot?.Attachments)}</span></h3></summary>
      
      <div  class="attachments">
        <div  class="row attachtools">
          <label  class="chip"><input  type="radio"  name="attshow"  value="all"  checked> All</label>
          <label  class="chip"><input  type="radio"  name="attshow"  value="doc"> Documents</label>
          <label  class="chip"><input  type="radio"  name="attshow"  value="image"> Pictures</label>
          <label  class="chip"><input  type="radio"  name="attshow"  value="video"> Videos</label>
          <label  class="chip"><input  type="checkbox"  class="atthide"> Hide previews</label>
        </div>
        <ul  class="files">
          {#each F.rng(dot?.Attachments) as [k2, d2]}{@const v_kind = F.fileKind(d2?.ContentType)}
            
            <li  data-kind={S(v_kind)}>
              <a  href={("/documents/" + S(dot?.Doc?.ID) + "/files/" + S(d2?.FileID))}  target="_blank"  rel="noopener">{d2?.Filename}</a>
              {#if F.t(F.eq(v_kind, "image"))}
                <a  href={("/documents/" + S(dot?.Doc?.ID) + "/files/" + S(d2?.FileID))}  class="attachpreview-link"  target="_blank"  rel="noopener">
                  <img  class="attachpreview"  src={"/documents/" + S(dot?.Doc?.ID) + "/files/" + S(d2?.FileID)}  alt={S(d2?.Filename)}  loading="lazy">
                </a>
              {/if}
              {#if F.t(F.eq(v_kind, "video"))}
                <video  class="attachvideo"  controls preload="metadata"  src={"/documents/" + S(dot?.Doc?.ID) + "/files/" + S(d2?.FileID)}></video>
              {/if}
            </li>
          {/each}
        </ul>
      </div>
    </details>
  {/if}

  <h3>Comments</h3>
  <div  id="peek-comments">{@render comments(dot)}</div>
  <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/comments"}  class="stack composer">
    <input  type="hidden"  name="peek"  value="1">
    {@render mdbar(null)}
    <div  class="mdpair">
      <textarea  data-rich name="body"  rows="3"  required placeholder="Add a comment" data-preview></textarea>
      <div  class="mdpreview prose"  aria-live="polite"></div>
    </div>
    <div  class="row"><button  type="submit">Comment</button></div>
  </form>
</article>
{/snippet}
{#snippet fieldsrow(dot)}
  {#if F.t(dot?.Doc?.Task)}
    <form  class="fields fields-row"  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/status"} data-quiet onchange={(e) => e.currentTarget.requestSubmit()}>
      <input  type="hidden"  name="version"  value={S(dot?.Doc?.Version)}>

      <div  class="field">
        <label  for="status">Status</label>
        <select  id="status"  name="status">
          {#each F.rng(dot?.Statuses) as [k3, d3]}
            <option  value={S(d3)}  {...(F.t(F.eq(d3, dot?.Doc?.Task?.Status)) ? {"selected": true} : {})}>{F.statusLabel(d3)}</option>
          {/each}
        </select>
      </div>

      <div  class="field">
        <label  for="priority">Priority</label>
        <select  id="priority"  name="priority">
          <option  value="0"  {...(F.t(F.eq(dot?.Doc?.Task?.Priority, 0)) ? {"selected": true} : {})}>low</option>
          <option  value="1"  {...(F.t(F.eq(dot?.Doc?.Task?.Priority, 1)) ? {"selected": true} : {})}>normal</option>
          <option  value="2"  {...(F.t(F.eq(dot?.Doc?.Task?.Priority, 2)) ? {"selected": true} : {})}>high</option>
          <option  value="3"  {...(F.t(F.eq(dot?.Doc?.Task?.Priority, 3)) ? {"selected": true} : {})}>urgent</option>
        </select>
      </div>

      

      <div  class="field">
        <label  for="due">Due</label>
        <input  id="due"  type="date"  name="due"  value={S((F.t(dot?.Doc?.Task?.DueAt) ? (S(F.m(dot?.Doc?.Task?.DueAt, "Format", "2006-01-02"))) : ""))}>
      </div>

      {#if F.t(dot?.Doc?.Task?.CompletedAt)}
        <span  class="muted small">completed {F.ago(dot?.Doc?.Task?.CompletedAt)}</span>
      {/if}
    </form>
  {/if}
{/snippet}
{#snippet comments(dot)}
  {#if F.t(dot?.Comments)}
    <ul  class="messages">
      {#each F.rng(dot?.Comments) as [k4, d4]}
        <li  data-message={S(d4?.ID)}  class="comment">
          <img  class="avatar"  src={"/people/" + S(d4?.AuthorID) + "/avatar"}  alt=""  loading="lazy"  width="32"  height="32">
          <div  class="commentbody">
          <div  class="row commenthead">
            <span  class="author">{d4?.AuthorName}</span>
            <span  class="muted small">{F.ago(d4?.CreatedAt)}{#if F.t(d4?.EditedAt)} · edited{/if}</span>
          </div>
          <div  class="prose">{@html d4?.Body_md}</div>
          {#if F.t(d4?.Mentions)}
            <p  class="mentions">
              {#each F.rng(d4?.Mentions) as [k5, d5]}
                {#if F.t(F.eq(F.printf("%s", d5?.Type), "document"))}
                  <a  class="chip"  href={("/documents/" + S(d5?.ID))}>{d5?.Label}</a>
                {:else}
                  <span  class="chip">{d5?.Label}</span>
                {/if}
              {/each}
            </p>
          {/if}
          
          {#if F.t(F.and(dot?.Identity, F.eq(d4?.AuthorID, dot?.Identity?.PrincipalID)))}
            
            <button  type="button"  class="ghost small"  popovertarget={"pop-edit-" + S(d4?.ID)}>Edit</button>
            <div  id={"pop-edit-" + S(d4?.ID)}  popover class="popover popover-lg">
              <form  method="post"  action={"/messages/" + S(d4?.ID)}  class="stack">
                <input  type="hidden"  name="back"  value={"/documents/" + S(dot?.Doc?.ID)}>
                {@render mdbar(null)}
                <div  class="mdpair">
                  <textarea  data-rich name="body"  rows="8"  required data-preview>{d4?.Body}</textarea>
                  <div  class="mdpreview prose"  aria-live="polite"></div>
                </div>
                <div  class="row">
                  <button  type="submit"  class="ghost small">Save</button>
                  <span  class="help"  tabindex="0"                          title="Drag the bottom-right corner to resize. Formatting: **bold**, *italic*, [link](url), and a line starting with - for a list. A YouTube link on a line of its own plays as a video.">?</span>
                </div>
              </form>
              <form  method="post"  action={"/messages/" + S(d4?.ID) + "/delete"}  class="inline">
                <button  type="submit"  class="ghost small">Remove</button>
              </form>
            </div>
          {/if}
          </div>
        </li>
      {/each}
    </ul>
  {:else}
    <p  class="muted small">No comments yet.</p>
  {/if}
{/snippet}
{#snippet mdbar(dot)}
<div  class="mdbar"  role="toolbar"  aria-label="Formatting">
  <button  type="button"  class="ghost small"  data-md="bold"  title="Bold"><strong>B</strong></button>
  <button  type="button"  class="ghost small"  data-md="italic"  title="Italic"><em>I</em></button>
  <button  type="button"  class="ghost small"  data-md="link"  title="Link">Link</button>
  <button  type="button"  class="ghost small"  data-md="list"  title="List">&bull; List</button>
  <button  type="button"  class="ghost small"  data-md="checklist"  title="Checklist">&#9745; Checklist</button>
  <button  type="button"  class="ghost small"  data-md="video"  title="Insert a YouTube video">&#9654; Video</button>
  <button  type="button"  class="ghost small mdpreview-toggle"  data-md="preview"  title="See how it will look"  aria-pressed="false">&#128065; Preview</button>
  <button  type="button"  class="ghost small mdraw-toggle"  data-md="raw"  title="Write in Markdown instead"  aria-pressed="false">Markdown</button>
  <button  type="button"  class="ghost small mdbig-toggle"  data-md="big"  title="Write in a bigger box">&#10530; Open big</button>
</div>
{/snippet}

{#snippet content(dot: any)}
{#if F.t(dot?.Peek)}{@render peek(dot)}{:else}
<article  class="card">
  {#if F.t(dot?.TimeForm?.Error)}<p  class="notice"  role="alert"  data-time-error>Time was not logged: {dot?.TimeForm?.Error}</p>{/if}
  
  {#if F.t(dot?.ParentDoc)}{@const w6 = dot?.ParentDoc}
    <p  class="muted small">Part of <a  href={("/documents/" + S(w6?.ID))}>{#if F.t(w6?.Title)}{w6?.Title}{:else}Untitled{/if}</a></p>
  {/if}
  {#if F.t(dot?.Flash)}<p  class="card small"  role="status">{dot?.Flash}</p>{/if}
  <div  class="row spread" {...(F.t(dot?.Panel) ? {"data-drag-handle": true, "title": "Drag to move"} : {})}>
    <h1>{#if F.t(dot?.Doc?.Title)}{dot?.Doc?.Title}{:else}Untitled{/if}</h1>
    <div  class="row">
      <span  class="tag">{dot?.Doc?.Kind}</span>
      
      {#if F.t(dot?.Doc?.Task)}
        <button  type="button"  class="chip"  popovertarget="pop-assignees">{#if F.t(dot?.AssigneeIDs)}Assigned &middot; {F.len(dot?.AssigneeIDs)}{:else}Assign{/if}</button>
        {#if F.t(dot?.CanAssign)}
          <button  type="button"  class="chip"  popovertarget="pop-share">Share{#if F.t(dot?.Guests)} &middot; {F.len(dot?.Guests)}{/if}</button>
        {/if}
      {/if}
      {#if F.t(F.eq(dot?.Doc?.Kind, "daily_report"))}
        <a  class="chip"  href={("/documents/" + S(dot?.Doc?.ID) + "/report.pdf")}  download>Download PDF</a>
      {/if}
      <button  type="button"  class="chip"  popovertarget="pop-more"  aria-label="More actions">&#8943; More</button>
      <div  id="pop-more"  popover class="popover menupop">
        {#if F.t(dot?.Doc?.Task)}<button  type="button"  class="menuitem"  popovertarget="pop-time">Time</button>{/if}
        {#if F.t(dot?.Properties)}<button  type="button"  class="menuitem"  popovertarget="pop-fields">Fields</button>{/if}
        
        <button  type="button"  class="menuitem"  popovertarget="pop-space">Space</button>
        <button  type="button"  class="menuitem"  popovertarget="pop-edit-doc">Edit</button>
        
        <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/watch"}  class="inline">
          <button  type="submit"  class="menuitem">{#if F.t(dot?.Watching)}Stop watching{:else}Watch{/if}</button>
        </form>
        
        <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/delete"}  class="inline"                data-confirm={"Delete this " + S(dot?.Doc?.Kind) + "? It stops appearing in lists."}>
          <button  type="submit"  class="menuitem danger">Delete</button>
        </form>
      </div>
      
      {#if F.t(dot?.Panel)}
        <a  class="chip"  href={("/documents/" + S(dot?.Doc?.ID))}  target="_blank"  rel="noopener">Open full page ↗</a>
        <button  type="button"  class="chip"  popovertarget="taskpanel">Close</button>
      {/if}
    </div>
  </div>

  {#if F.t(dot?.From)}
    <p  class="muted small">
      Imported from {dot?.From?.Source}
      {#if F.t(dot?.From?.URL)}· <a  href={(S(dot?.From?.URL))}  rel="noreferrer noopener">original</a>{/if}
    </p>
  {/if}

  
  <div  class="taskgrid">
    <div  class="taskmain">
      {@render fieldsrow(dot)}

      
      {#if F.t(dot?.Doc?.Task)}
        <div  id="pop-assignees"  popover class="popover assignpop">
          <h3>Assigned to{#if F.t(dot?.AssigneeIDs)} <span  class="muted small">{F.len(dot?.AssigneeIDs)}</span>{/if}</h3>
          {#if F.t(dot?.CanAssign)}
            <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/assignees"}  class="stack">
              <input  type="search"  placeholder="Find a person"  aria-label="Find a person"  data-filter-rows=".assignrow">
              <p  class="muted small">Tick who is on this task. The &#9679; is the primary &mdash; the one it shows under on the board.</p>
              <div  class="assignrows"  data-checked-first>
                {#each F.rng(dot?.People) as [k7, d7]}
                  <div  class="assignrow">
                    <label  class="aname"><input  type="checkbox"  name="assignee"  value={S(d7?.ID)} {...(F.t(F.m(dot, "IsAssigned", d7?.ID)) ? {"checked": true} : {})}> {d7?.Name}</label>
                    <label  class="aprimary"  title="Primary (shows on the board)"><input  type="radio"  name="primary"  value={S(d7?.ID)} {...(F.t(F.m(dot, "IsPrimaryAssignee", d7?.ID)) ? {"checked": true} : {})}> primary</label>
                  </div>
                {/each}
              </div>
              <div  class="row"><button  type="submit"  class="ghost small">Save assignees</button></div>
            </form>
          {:else if F.t(dot?.AssigneeIDs)}
            <ul  class="files">
              {#each F.rng(dot?.People) as [k8, d8]}{#if F.t(F.m(dot, "IsAssigned", d8?.ID))}
                <li>{d8?.Name}{#if F.t(F.m(dot, "IsPrimaryAssignee", d8?.ID))} <span  class="tag small">primary</span>{/if}</li>
              {/if}{/each}
            </ul>
          {:else}
            <p  class="muted small">Nobody is assigned yet.</p>
          {/if}
        </div>
      {/if}

      
      {#if F.t(F.and(dot?.Doc?.Task, dot?.CanAssign))}
        <div  id="pop-share"  popover class="popover">
          <h3>Shared with{#if F.t(dot?.Guests)} <span  class="muted small">{F.len(dot?.Guests)}</span>{/if}</h3>
          {#if F.t(dot?.Guests)}
            <ul  class="files">
              {#each F.rng(dot?.Guests) as [k9, d9]}
                <li  class="row spread">
                  <span>{d9?.Name} <span  class="tag small">{d9?.Level}</span></span>
                  <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/guests/remove"}  class="inline">
                    <input  type="hidden"  name="person"  value={S(d9?.PersonID)}>
                    <button  type="submit"  class="ghost small"  title="Remove">&times;</button>
                  </form>
                </li>
              {/each}
            </ul>
          {:else}
            <p  class="muted small">Only the people working on it can see it.</p>
          {/if}
          <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/guests"}  class="stack">
            <select  name="person"  required aria-label="Person">
              <option  value="">Add someone&hellip;</option>
              {#each F.rng(dot?.People) as [k10, d10]}<option  value={S(d10?.ID)}>{d10?.Name}</option>{/each}
            </select>
            <select  name="level"  aria-label="As">
              <option  value="view">Viewer (read only)</option>
              <option  value="comment">Commenter</option>
              <option  value="edit">Editor</option>
            </select>
            <button  type="submit"  class="ghost small">Share</button>
          </form>
        </div>
      {/if}

      {#if F.t(dot?.Properties)}
        
        <div  id="pop-fields"  popover class="popover popover-lg">
          <h3>Fields</h3>
          <form  class="fields"  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/properties"}>
            {#each F.rng(dot?.Properties) as [k11, d11]}{@const v_p = d11}
              <label  for={"prop-" + S(v_p?.Key)}>{v_p?.Label}</label>
              {#if F.t(F.eq(v_p?.DataType, "select"))}
                <select  id={"prop-" + S(v_p?.Key)}  name={"prop:" + S(v_p?.ID)}>
                  <option  value="">Not set</option>
                  {#each F.rng(v_p?.Options) as [k12, d12]}{@const v_opt = d12}
                    <option  value={S(v_opt)}  {...(F.t(F.eq(v_opt, v_p?.Value)) ? {"selected": true} : {})}>{v_opt}</option>
                  {/each}
                </select>
              {:else if F.t(F.eq(v_p?.DataType, "number"))}
                <input  id={"prop-" + S(v_p?.Key)}  name={"prop:" + S(v_p?.ID)}  type="number"  step="any"  value={S(v_p?.Value)}>
              {:else if F.t(F.eq(v_p?.DataType, "date"))}
                <input  id={"prop-" + S(v_p?.Key)}  name={"prop:" + S(v_p?.ID)}  type="date"  value={S(v_p?.Value)}>
              {:else}
                <input  id={"prop-" + S(v_p?.Key)}  name={"prop:" + S(v_p?.ID)}  value={S(v_p?.Value)}>
              {/if}
            {/each}
            <div  class="row"><button  type="submit">Save fields</button></div>
          </form>
        </div>
      {/if}

      
      <div  id="pop-space"  popover class="popover">
        <h3>Space
          <span  class="help"  tabindex="0"                  title="Files this task into a department/folder, or moves it out of the one it's already in. It lives in exactly one space at a time -- this is not a shortcut or a second listing.">?</span>
        </h3>
        <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/space"}  class="stack"  data-space-form>
          <select  id="space"  name="space">
            <option  value="">Not filed</option>
            {#each F.rng(dot?.Spaces) as [k13, d13]}
              <option  value={S(d13?.ID)}  {...(F.t(F.same(d13?.ID, dot?.Doc?.SpaceID)) ? {"selected": true} : {})}>{d13?.Path}</option>
            {/each}
          </select>
          <button  type="submit"  class="ghost small">File</button>
        </form>
      </div>

      
      <div  id="pop-edit-doc"  popover class="popover popover-lg">
        <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID)}  class="stack">
          <input  type="hidden"  name="version"  value={S(dot?.Doc?.Version)}>
          <label  for="title">Title</label>
          <input  id="title"  name="title"  value={S(dot?.Doc?.Title)}>
          <label  for="body">Details
            <span  class="help"  tabindex="0"                    title="Drag the bottom-right corner to resize. Formatting: **bold**, *italic*, [link](url), and a line starting with - for a list. A YouTube link on a line of its own plays as a video.">?</span>
          </label>
          {@render mdbar(null)}
          <div  class="mdpair">
            <textarea  data-rich id="body"  name="body"  rows="10" data-preview data-preview-cards>{dot?.Doc?.Body}</textarea>
            <div  class="mdpreview prose"  aria-live="polite"></div>
          </div>
          <div  class="row">
            <button  type="submit"  class="primary">Save</button>
            <span  class="muted small">
              <a  href={("/documents/" + S(dot?.Doc?.ID) + "/history")}>Version {dot?.Doc?.Version}</a>
              · updated {F.ago(dot?.Doc?.UpdatedAt)} · owner {F.short(dot?.Doc?.OwnerID)}
            </span>
          </div>
        </form>
      </div>

      
      <details  class="fold"  open>
        <summary><h3>Details</h3></summary>
        {#if F.t(dot?.BodyHTML)}
          <div  class="prose">{@html dot?.BodyHTML}</div>
        {:else}
          <p  class="muted small">No details yet.</p>
        {/if}
      </details>

      
      {#if F.t(F.hasTaskFields(dot?.Doc?.Kind))}
        <details  class="fold"  id="subtasks" {...(F.t(dot?.Subtasks) ? {"open": true} : {})}>
          <summary><h3>Subtasks{#if F.t(dot?.Subtasks)} <span  class="muted small">{F.len(dot?.Subtasks)}</span>{/if}</h3></summary>
          {#if F.t(dot?.Subtasks)}
            <ul  class="files">
              {#each F.rng(dot?.Subtasks) as [k14, d14]}
                <li><a  href={("/documents/" + S(d14?.ID))}>{#if F.t(d14?.Title)}{d14?.Title}{:else}Untitled{/if}</a>
                  {#if F.t(d14?.Task)} <span  class={"status " + S(d14?.Task?.Status)}>{F.statusLabel(d14?.Task?.Status)}</span>{/if}</li>
              {/each}
            </ul>
          {/if}
          <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/subtasks"}  class="row">
            <input  name="title"  placeholder="Add a subtask"  required aria-label="Subtask title"  style="flex:1">
            <button  type="submit"  class="ghost small">Add</button>
          </form>
        </details>
      {/if}

      
      {#if F.t(F.not(F.writable(dot?.Doc?.Kind)))}
      <h3  class="row spread">
        <span>Checklist{#if F.t(dot?.Progress?.Total)}
          <span  class="muted small">{dot?.Progress?.Done} of {dot?.Progress?.Total}</span>{/if}</span>
        <button  type="button"  class="chip"  popovertarget="pop-add-step">Add</button>
      </h3>
      {#if F.t(dot?.Checklist)}
        <ul  class="checklist">
          {#each F.rng(dot?.Checklist) as [k15, d15]}
            <li  class={S((F.t(d15?.Done) ? ("ticked") : ""))}>
              <div  class="row">
                
                <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/checklist/" + S(d15?.ID)}  class="inline">
                  <input  type="hidden"  name="done"  value={S((F.t(d15?.Done) ? ("0") : ("1")))}>
                  <button  type="submit"  class={"tick" + S((F.t(d15?.Done) ? (" on") : ""))}  aria-label={S((F.t(d15?.Done) ? ("Mark not done") : ("Mark done")))}>{#if F.t(d15?.Done)}&#10003;{/if}</button>
                </form>
                <span  class="text">{d15?.Text}</span>
                {#if F.t(d15?.Done)}<span  class="muted small">{d15?.DoneName} · {F.ago(d15?.DoneAt)}</span>{/if}
                <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/checklist/" + S(d15?.ID) + "/delete"}  class="inline">
                  <button  type="submit"  class="ghost small">Remove</button>
                </form>
              </div>
              
              {#if F.t(d15?.Description)}
                <details  class="stepnote">
                  <summary>Details</summary>
                  <p  class="muted small">{d15?.Description}</p>
                </details>
              {/if}
            </li>
          {/each}
        </ul>
      {:else}
        <p  class="muted small">No steps yet.</p>
      {/if}
      
      <div  id="pop-add-step"  popover {...(F.t(dot?.ChecklistOpen) ? {"open": true} : {})}  class="popover popover-lg">
        <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/checklist"}  class="stack">
          <label  for="step">Step</label>
          <input  id="step"  name="text"  placeholder="Step"  required>
          <label  for="stepdesc">Description (optional)
            <span  class="help"  tabindex="0"                    title="Drag the bottom-right corner of the box to make it bigger or smaller.">?</span>
          </label>
          <textarea  id="stepdesc"  name="description"  rows="5"></textarea>
          <div  class="row"><button  type="submit">Add</button></div>
        </form>
      </div>
      {/if}

      <h3>Attachments</h3>
      {#if F.t(dot?.Attachments)}
        
        <div  class="attachments">
          <div  class="row attachtools">
            <label  class="chip"><input  type="radio"  name="attshow"  value="all"  checked> All</label>
            <label  class="chip"><input  type="radio"  name="attshow"  value="doc"> Documents</label>
            <label  class="chip"><input  type="radio"  name="attshow"  value="image"> Pictures</label>
            <label  class="chip"><input  type="radio"  name="attshow"  value="video"> Videos</label>
            <label  class="chip"><input  type="checkbox"  class="atthide"> Hide previews</label>
          </div>
          <ul  class="files">
            {#each F.rng(dot?.Attachments) as [k16, d16]}{@const v_kind = F.fileKind(d16?.ContentType)}
              
              <li  data-kind={S(v_kind)}>
                <a  href={("/documents/" + S(dot?.Doc?.ID) + "/files/" + S(d16?.FileID))}>{d16?.Filename}</a>
                <span  class="muted small">{d16?.Size} bytes</span>
                <code  class="muted small">{F.short(d16?.Digest)}</code>
                <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/files/" + S(d16?.FileID) + "/remove"}  class="inline"                        data-confirm={"Remove " + S(d16?.Filename) + "? You can restore it from Removed files."}>
                  <button  type="submit"  class="ghost small">Remove</button>
                </form>
                {#if F.t(F.eq(v_kind, "image"))}
                  <a  href={("/documents/" + S(dot?.Doc?.ID) + "/files/" + S(d16?.FileID))}  class="attachpreview-link">
                    <img  class="attachpreview"  src={"/documents/" + S(dot?.Doc?.ID) + "/files/" + S(d16?.FileID)}  alt={S(d16?.Filename)}  loading="lazy">
                  </a>
                {/if}
                {#if F.t(F.eq(v_kind, "video"))}
                  <video  class="attachvideo"  controls preload="metadata"  src={"/documents/" + S(dot?.Doc?.ID) + "/files/" + S(d16?.FileID)}></video>
                  
                  <button  type="button"  class="ghost small videopop-open"  popovertarget={"vp-" + S(d16?.FileID)}>Open larger &#8599;</button>
                  <div  id={"vp-" + S(d16?.FileID)}  popover="manual"  class="popover videopop">
                    <div  class="videopop-bar"  data-drag-handle title="Drag to move">
                      <span  class="muted small">{d16?.Filename} &middot; drag to move, resize from the corner</span>
                      <button  type="button"  class="ghost small"  popovertarget={"vp-" + S(d16?.FileID)}  popovertargetaction="hide">Close</button>
                    </div>
                    <div  class="videopop-frame"><video  controls preload="none"  src={"/documents/" + S(dot?.Doc?.ID) + "/files/" + S(d16?.FileID)}></video></div>
                  </div>
                {/if}
              </li>
            {/each}
          </ul>
        </div>
      {:else}
        <p  class="muted small">No files attached.</p>
      {/if}
      
      {#if F.t(dot?.RemovedAttachments)}
        <details  class="removedfiles">
          <summary  class="muted small">Removed files ({F.len(dot?.RemovedAttachments)})</summary>
          <ul  class="files">
            {#each F.rng(dot?.RemovedAttachments) as [k17, d17]}
              <li>
                <span  class="muted">{d17?.Filename}</span>
                <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/files/" + S(d17?.FileID) + "/restore"}  class="inline">
                  <button  type="submit"  class="ghost small">Restore</button>
                </form>
              </li>
            {/each}
          </ul>
        </details>
      {/if}

      
      
      <div  class="uploadrow">
      <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/files"}              enctype="multipart/form-data"  class="row"  data-capture data-quickupload>
        <label  for="photo"  class="muted">Take a photo</label>
        
        <span  class="help"  tabindex="0"                title="Opens the camera directly on a phone. On a desktop it's just another way to attach a file.">?</span>
        <input  id="photo"  type="file"  name="file"  accept="image/*"                 capture="environment"  required>
        <button  type="submit"  class="big">Take a photograph</button>
      </form>

      <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/files"}              enctype="multipart/form-data"  class="row"  data-quickupload>
        <label  for="attachment"  class="muted">Attach a file</label>
        <input  id="attachment"  type="file"  name="file"  required>
        <button  type="submit">Attach</button>
      </form>
      </div>
    </div>

    <div  class="taskside">
      <h3>Comments</h3>
      {#if F.t(dot?.CommentRemoved)}
        
        <p  class="warn row spread">
          <span>Removed a comment.</span>
          </p><form  method="post"  action={"/messages/" + S(dot?.CommentRemoved) + "/restore"}  class="inline">
            <input  type="hidden"  name="back"  value={"/documents/" + S(dot?.Doc?.ID)}>
            <button  type="submit"  class="ghost small">Undo</button>
          </form>
        <p></p>
      {/if}
      <div  id="comments"  data-live>
        {@render comments(dot)}
      </div>
      <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/comments"}  class="stack composer">
        {@render mdbar(null)}
        <div  class="mdpair">
          <textarea  data-rich name="body"  rows="4"  required placeholder="Add a comment"  data-grow-up data-preview></textarea>
          <div  class="mdpreview prose"  aria-live="polite"></div>
        </div>
        <div  class="row">
          <button  type="submit">Comment</button>
          <span  class="help"  tabindex="0"                  title="Drag the bottom-right corner to resize. Formatting: **bold**, *italic*, [link](url), and a line starting with - for a list. A YouTube link on a line of its own plays as a video.">?</span>
        </div>
      </form>

      
      {#if F.t(dot?.MailThread)}
        <h3>Email</h3>
        <ul  class="files"  data-mail-thread>
          {#each F.rng(dot?.MailThread) as [k18, d18]}
            <li>
              <span  class="tag">{#if F.t(d18?.Inbound)}in{:else}out{/if}</span>
              <strong>{d18?.Subject}</strong>
              <span  class="muted small">
                {#if F.t(d18?.Inbound)}{d18?.From}{:else}to {d18?.To}{/if} · {F.date(d18?.At)}</span>
            </li>
          {/each}
        </ul>
        <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/reply"}  class="stack">
          <textarea  name="body"  rows="4"  placeholder="Reply by email"  required></textarea>
          <div  class="row">
            <button  type="submit">Send reply</button>
            <span  class="muted small">Threads under the original in their client,
              so the sender never learns a new tool.</span>
          </div>
        </form>
      {/if}

      
      {#if F.t(F.or(F.eq(dot?.Doc?.Kind, "work_order"), F.eq(dot?.Doc?.Kind, "purchase_order")))}
        <h3>Calls</h3>
        {#if F.t(dot?.Calls)}
          <ul  class="files"  data-calls>
            {#each F.rng(dot?.Calls) as [k19, d19]}
              <li  data-call={S(d19?.ID)}>
                <span  class="tag">{#if F.t(F.eq(d19?.Direction, "in"))}in{:else}out{/if}</span>
                <strong>{#if F.t(d19?.ContactID)}<a  href={("/contacts/" + S(F.deref_s(d19?.ContactID)))}>{d19?.ContactName}</a>{:else}{d19?.Number}{/if}</strong>
                <span  class={"status " + S((F.t(d19?.Answered) ? ("done") : ("blocked")))}>{d19?.Outcome}</span>
                {#if F.t(d19?.DurationS)}<span  class="muted small">{d19?.Minutes}m</span>{/if}
                <span  class="muted small">{F.date(d19?.StartedAt)}{#if F.t(d19?.ByName)} · {d19?.ByName}{/if}</span>
                {#if F.t(d19?.Note)}<span  class="muted small">{d19?.Note}</span>{/if}
                {#if F.t(d19?.RecordingID)}<span  class="muted small">recording: {d19?.Recording}</span>{/if}
                <form  method="post"  action={"/calls/" + S(d19?.ID) + "/delete"}  class="inline">
                  <input  type="hidden"  name="back"  value={"/documents/" + S(dot?.Doc?.ID)}>
                  <button  type="submit"  class="ghost small">Remove</button>
                </form>
              </li>
            {/each}
          </ul>
        {:else}
          <p  class="muted small">No calls logged against this.</p>
        {/if}
        <form  method="post"  action="/calls"  class="row wrap">
          <input  type="hidden"  name="document"  value={S(dot?.Doc?.ID)}>
          <input  type="hidden"  name="back"  value={"/documents/" + S(dot?.Doc?.ID)}>
          <select  name="direction"  aria-label="Direction">
            <option  value="in">they rang</option>
            <option  value="out">we rang</option>
          </select>
          <select  name="contact"  aria-label="Contact">
            <option  value="">Not on file</option>
            {#each F.rng(dot?.Contacts) as [k20, d20]}<option  value={S(d20?.ID)}>{d20?.Name}</option>{/each}
          </select>
          <input  name="number"  placeholder="number"  style="width:9rem"  aria-label="Number">
          <select  name="outcome"  aria-label="Outcome">
            {#each F.rng(dot?.CallOutcomes) as [k21, d21]}<option  value={S(d21)}>{d21}</option>{/each}
          </select>
          <input  name="minutes"  type="number"  min="0"  placeholder="mins"  style="width:5rem"                   aria-label="Minutes">
          <input  name="at"  type="datetime-local"  aria-label="When">
          <input  name="note"  placeholder="what was said"  aria-label="Note">
          <button  type="submit">Log a call</button>
        </form>
      {/if}

      {#if F.t(dot?.Doc?.Task)}
      
      <div  id="pop-time"  popover class="popover popover-lg">
        <h3>Time
          <span  class="help"  tabindex="0"                  title="Actual adds up the timer and every entry below automatically. Estimate is an optional target to check it against. &quot;Or log&quot; adds a finished chunk of time by hand, for work already done rather than happening now.">?</span>
        </h3>
        {#if F.t(dot?.JustRemoved)}
          
          <p  class="warn row spread">
            <span>Removed an entry.</span>
            </p><form  method="post"  action={"/time/entries/" + S(dot?.JustRemoved) + "/restore"}  class="inline">
              <input  type="hidden"  name="back"  value={"/documents/" + S(dot?.Doc?.ID)}>
              <button  type="submit"  class="ghost small">Undo</button>
            </form>
          <p></p>
        {/if}
        {#if F.t(dot?.Effort)}{@const w22 = dot?.Effort}
          <p  class="muted small">
            {#if F.t(w22?.EstimateMinutes)}Estimated {F.hours(F.deref(w22?.EstimateMinutes))}, {/if}
            actual {F.hours(w22?.ActualMinutes)}
            {#if F.t(w22?.HasEstimate)}
              <span  class={S((F.t(F.gt(w22?.OverPercent, 0)) ? ("error") : ""))}>
                ({#if F.t(F.gt(w22?.OverPercent, 0))}+{/if}{w22?.OverPercent}% against the estimate)</span>
            {/if}
            
            {#if F.t(w22?.CostCents)} · cost {F.money(w22?.CostCents)}{/if}
          </p>
        {/if}

        <form  class="row"  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/estimate"}>
          <label  for="estimate"  class="muted small">Estimate</label>
          {@render hoursmins("estimate_hours", "estimate_mins", dot?.TimeForm?.Estimate ? dot?.TimeForm?.Estimate?.Hours : (dot?.Effort?.EstimateMinutes != null ? Math.floor(F.deref(dot?.Effort?.EstimateMinutes) / 60) : ''), dot?.TimeForm?.Estimate ? dot?.TimeForm?.Estimate?.Mins : (dot?.Effort?.EstimateMinutes != null ? F.deref(dot?.Effort?.EstimateMinutes) % 60 : ''), dot?.TimeForm?.Estimate?.Error, "estimate-error")}
          <button  type="submit"  class="ghost small">Save estimate</button>
        </form>

        {#if F.t(dot?.Timer)}
          {#if F.t(F.eq(dot?.Timer?.DocumentID, dot?.Doc?.ID))}
            <p  class="muted small">A timer has been running on this since {F.ago(dot?.Timer?.StartedAt)}.</p>
          {:else}
            <p  class="warn">
              Your timer is running on <a  href={("/documents/" + S(dot?.Timer?.DocumentID))}>{dot?.Timer?.DocTitle}</a>.
              Stop it before starting another, so the same hour is not on two jobs.
            </p>
          {/if}
        {:else}
          <form  class="row"  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/timer"}>
            <input  name="note"  placeholder="What are you doing?"  style="max-width:18rem">
            <button  type="submit"  class="primary">Start a timer</button>
          </form>
        {/if}

        
        <form  class="stack"  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/time"}  data-time-form>
          <span  class="muted small">Or log a finished chunk of time</span>
          {@render hoursmins("hours", "mins", dot?.TimeForm?.Hours, dot?.TimeForm?.Mins, dot?.TimeForm?.Error, "minutes-error")}
          <input  name="on"  type="date"  aria-label="Date"  value={S(dot?.TimeForm?.On)}>
          <input  name="note"  placeholder="Note"  value={S(dot?.TimeForm?.Note)}>
          {#if F.t(dot?.Tags)}
            <span  class="muted small">Tags:
              {#each F.rng(dot?.Tags) as [k23, d23]}
                <label  class="chip"><input  type="checkbox"  name="tags"  value={S(d23?.ID)}  checked={!!dot?.TimeForm?.Tags?.includes(d23?.ID)}> {d23?.Name}</label>
              {/each}
            </span>
          {/if}
          <button  type="submit">Log time</button>
        </form>

        {#if F.t(dot?.TimeEntries)}
          <ul  class="files">
            {#each F.rng(dot?.TimeEntries) as [k24, d24]}
              <li  data-entry={S(d24?.ID)}>
                <span>{d24?.PersonName}</span>
                <span>{F.hours(d24?.Elapsed)}{#if F.t(d24?.Running)} <span  class="tag">running</span>{/if}</span>
                <span  class="muted small">{F.date(d24?.StartedAt)}{#if F.t(d24?.Note)} · {d24?.Note}{/if}</span>
                {#if F.t(F.not(d24?.Running))}{@const v_entryID = d24?.ID}
                  <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/time/" + S(d24?.ID)}  class="inline row">
                    {@render hoursmins("hours", "mins", dot?.TimeForm?.Fix?.ID === d24?.ID ? dot?.TimeForm?.Fix?.Hours : Math.floor((d24?.Minutes ?? 0) / 60), dot?.TimeForm?.Fix?.ID === d24?.ID ? dot?.TimeForm?.Fix?.Mins : (d24?.Minutes ?? 0) % 60, dot?.TimeForm?.Fix?.ID === d24?.ID ? dot?.TimeForm?.Fix?.Error : '', "fix-error-" + d24?.ID)}
                    <input  name="on"  type="date"  value={S(F.dateinput(d24?.StartedAt))}  style="width:9.5rem"                             aria-label="Corrected day">
                    <button  type="submit"  class="ghost small">Correct</button>
                  </form>
                  <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/time/" + S(d24?.ID) + "/delete"}  class="inline">
                    <button  type="submit"  class="ghost small">Remove</button>
                  </form>
                  
                  
                  <span  class="row"  style="flex-wrap:wrap">
                    {#each F.rng(d24?.Tags) as [k25, d25]}
                      <form  method="post"  action={"/time/entries/" + S(v_entryID) + "/tags/" + S(d25?.ID) + "/delete"}  class="inline">
                        <input  type="hidden"  name="back"  value={"/documents/" + S(dot?.Doc?.ID)}>
                        <button  type="submit"  class="tag"  title="Remove tag">{d25?.Name} ×</button>
                      </form>
                    {/each}
                    {#if F.t(dot?.Tags)}
                      <form  method="post"  action={"/time/entries/" + S(v_entryID) + "/tags"}  class="inline">
                        <input  type="hidden"  name="back"  value={"/documents/" + S(dot?.Doc?.ID)}>
                        <select  name="tag"  aria-label="Add a tag"  data-autosubmit>
                          <option  value="">+ tag</option>
                          {#each F.rng(dot?.Tags) as [k26, d26]}<option  value={S(d26?.ID)}>{d26?.Name}</option>{/each}
                        </select>
                      </form>
                    {/if}
                  </span>
                {/if}
              </li>
            {/each}
          </ul>
        {/if}
      </div>
      {/if}

      <h3>Related</h3>
      {#if F.t(dot?.Links)}
        <ul  class="files">
          {#each F.rng(dot?.Links) as [k27, d27]}
            <li>
              <a  href={("/documents/" + S(d27?.ID))}>{#if F.t(d27?.Title)}{d27?.Title}{:else}Untitled{/if}</a>
              <span  class="tag">{d27?.Kind}</span>
              {#if F.t(d27?.Task)}<span  class={"status " + S(d27?.Task?.Status)}>{F.statusLabel(d27?.Task?.Status)}</span>{/if}
              <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/unlink"}  class="inline">
                <input  type="hidden"  name="target"  value={S(d27?.ID)}>
                <button  type="submit"  class="ghost small">Remove</button>
              </form>
            </li>
          {/each}
        </ul>
      {:else}
        <p  class="muted small">Nothing linked yet.</p>
      {/if}

      {#if F.t(dot?.Assignable)}
        <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/link"}  class="row">
          <select  name="target">
            {#each F.rng(dot?.Assignable) as [k28, d28]}
              {#if F.t(F.ne(d28?.ID, dot?.Doc?.ID))}
                <option  value={S(d28?.ID)}>{#if F.t(d28?.Title)}{d28?.Title}{:else}Untitled{/if} ({d28?.Kind})</option>
              {/if}
            {/each}
          </select>
          <button  type="submit">Link</button>
        </form>
      {/if}

      
      {#if F.t(dot?.Parties)}
        <h3>Who is involved</h3>
        <ul  class="files"  data-parties>
          {#each F.rng(dot?.Parties) as [k29, d29]}
            <li>
              <a  href={(S(d29?.Href))}>{d29?.Name}</a>
              <span  class="tag">{d29?.Type}</span>
              {#if F.t(d29?.Subtext)}<span  class="muted small">{d29?.Subtext}</span>{/if}
            </li>
          {/each}
        </ul>
      {/if}

      {#if F.t(dot?.MentionedIn)}
        
        <h3>Mentioned in</h3>
        <ul  class="messages">
          {#each F.rng(dot?.MentionedIn) as [k30, d30]}
            <li>
              <div  class="row spread">
                <span  class="author">{d30?.AuthorName}</span>
                <span  class="muted small">{F.ago(d30?.CreatedAt)}</span>
              </div>
              <p  class="body">{d30?.Body}</p>
              {#if F.t(d30?.ChannelID)}<a  class="chip"  href={("/channels/" + S(d30?.ChannelID))}>in a channel</a>{/if}
            </li>
          {/each}
        </ul>
      {/if}

      
      {#if F.t(dot?.Channels)}
        <h3>Share with a channel</h3>
        <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/channel"}  class="row">
          <select  name="channel">
            {#each F.rng(dot?.Channels) as [k31, d31]}
              {#if F.t(d31?.Member)}<option  value={S(d31?.ID)}>{d31?.Name}</option>{/if}
            {/each}
          </select>
          <button  type="submit">Attach</button>
        </form>
        <p  class="muted small">
          Everybody in that channel gains this document. Anybody who leaves loses it.
        </p>
      {/if}
    </div>
  </div>

  
  {#if F.t(F.or(F.eq(dot?.Doc?.Kind, "work_order"), F.eq(dot?.Doc?.Kind, "purchase_order")))}
    <h3>Parts
      {#if F.t(dot?.PartsCents)}<span  class="muted small"  data-parts-cents={S(dot?.PartsCents)}>
        {F.cost(dot?.PartsCents)}</span>{/if}</h3>
    {#if F.t(dot?.PartsUsed)}
      <ul  class="files"  data-parts-used>
        {#each F.rng(dot?.PartsUsed) as [k32, d32]}
          <li  data-movement={S(d32?.ID)}>
            <strong>{F.qty(d32?.Quantity)}</strong>
            <a  href={("/parts")}>{d32?.PartName}</a>
            {#if F.t(d32?.SKU)}<span  class="muted small">{d32?.SKU}</span>{/if}
            <span  class="tag">{d32?.Reason}</span>
            <span  class="muted small">{F.cost(d32?.CostCents)}</span>
            <span  class="muted small">{F.date(d32?.MovedAt)}{#if F.t(d32?.ByName)} · {d32?.ByName}{/if}</span>
            {#if F.t(d32?.Note)}<span  class="muted small">{d32?.Note}</span>{/if}
            <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/parts/" + S(d32?.ID) + "/delete"}  class="inline">
              <button  type="submit"  class="ghost small">Remove</button>
            </form>
          </li>
        {/each}
      </ul>
    {:else}
      <p  class="muted small">No parts recorded against this.</p>
    {/if}
    <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/parts"}  class="row wrap">
      <select  name="part"  required aria-label="Part">
        <option  value="">Which part</option>
        {#each F.rng(dot?.Parts) as [k33, d33]}
          <option  value={S(d33?.ID)}>{d33?.Name}{#if F.t(d33?.SKU)} · {d33?.SKU}{/if} ({F.qty(d33?.OnHand)} on hand)</option>
        {/each}
      </select>
      <input  name="quantity"  type="number"  step="any"  min="0"  placeholder="qty"               style="width:5rem"  required aria-label="Quantity">
      <input  name="note"  placeholder="note"  aria-label="Note">
      <button  type="submit"  name="action"  value="consume">Fit</button>
      <button  type="submit"  name="action"  value="return"  class="ghost small">Return</button>
      {#if F.t(F.eq(dot?.Doc?.Kind, "purchase_order"))}
        <input  name="cost"  placeholder="unit cost"  style="width:7rem"  aria-label="Unit cost">
        <button  type="submit"  name="action"  value="receive"  class="ghost small">Book in</button>
      {/if}
    </form>
    <p  class="muted small">
      The unit cost is copied onto the movement as it is recorded. A price rise
      next spring must not change what this repair is on file as having cost.
    </p>
  {/if}

  {#if F.t(dot?.AssetTime)}
    
    <h3>Maintenance effort</h3>
    <p  class="muted small"  data-asset-minutes={S(dot?.AssetTime?.Minutes)}>
      {F.hours(dot?.AssetTime?.Minutes)} across {dot?.AssetTime?.WorkOrders}
      work order{#if F.t(F.ne(dot?.AssetTime?.WorkOrders, 1))}s{/if}
      {#if F.t(dot?.AssetTime?.CostCents)} · {F.money(dot?.AssetTime?.CostCents)}{/if}
    </p>
  {/if}

  {#if F.t(F.eq(dot?.Doc?.Kind, "asset"))}{@const v_parent = dot?.AssetFields?.ParentAssetID}
    <h3>Where it sits</h3>
    {#if F.t(dot?.AssetPath)}
      <p  class="muted small"  data-asset-path>
        {#each F.rng(dot?.AssetPath) as [k34, d34]}<a  href={("/documents/" + S(d34?.ID))}>{d34?.Title}</a> / {/each}
        <strong>{dot?.Doc?.Title}</strong>
      </p>
    {/if}
    <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/asset"}  class="row wrap">
      <select  name="parent"  aria-label="Parent asset">
        <option  value="">No parent</option>
        
        {#each F.rng(dot?.Assets) as [k35, d35]}
          {#if F.t(F.ne(d35?.ID, dot?.Doc?.ID))}
            <option  value={S(d35?.ID)}  {...(F.t(F.same(d35?.ID, v_parent)) ? {"selected": true} : {})}>{d35?.Path}</option>
          {/if}
        {/each}
      </select>
      <input  name="code"  value={S(dot?.AssetFields?.Code)}  placeholder="CV-003"               style="width:8rem"  aria-label="Asset code">
      <input  name="installed"  type="date"  value={S(F.datefield(dot?.AssetFields?.InstalledAt))}               aria-label="Installed">
      <input  name="retired"  type="date"  value={S(F.datefield(dot?.AssetFields?.RetiredAt))}               aria-label="Retired">
      <button  type="submit">Save</button>
      {#if F.t(dot?.AssetFields?.Code)}
        <a  class="ghost small"  href={("/documents/" + S(dot?.Doc?.ID) + "/label")}>Print label</a>
      {/if}
    </form>

    {#if F.t(dot?.Asset)}
      <p  class="muted small"  data-rollup>
        <span  data-open-rolled={S(dot?.Asset?.RolledOpen)}>{dot?.Asset?.RolledOpen} open</span>
        {#if F.t(F.ne(dot?.Asset?.RolledOpen, dot?.Asset?.OwnOpen))}
          <span  class="muted small">({dot?.Asset?.OwnOpen} on this asset, the rest below it)</span>
        {/if}
        · <span  data-minutes-rolled={S(dot?.Asset?.RolledMinutes)}>{F.hours(dot?.Asset?.RolledMinutes)}</span>
        {#if F.t(dot?.Asset?.RolledCost)} · {F.cost(dot?.Asset?.RolledCost)}{/if}
        {#if F.t(dot?.Asset?.RolledDowntime)}
          · <span  data-downtime-rolled={S(dot?.Asset?.RolledDowntime)}>{F.hours(dot?.Asset?.RolledDowntime)} down</span>
        {/if}
      </p>
    {/if}

    {#if F.t(dot?.Children)}
      <ul  class="files"  data-children>
        {#each F.rng(dot?.Children) as [k36, d36]}
          <li>
            <a  href={("/documents/" + S(d36?.ID))}>{d36?.Title}</a>
            {#if F.t(d36?.Code)}<span  class="muted small">{d36?.Code}</span>{/if}
            <span  class="muted small">{d36?.RolledOpen} open</span>
            {#if F.t(d36?.Down)}<span  class="status blocked">down</span>{/if}
          </li>
        {/each}
      </ul>
    {/if}

    <h3>Downtime</h3>
    {#if F.t(dot?.Uptime)}{@const w37 = dot?.Uptime}
      <p  class="muted small"  data-uptime={S(w37?.Percent)}>
        {w37?.Percent}% available over the last quarter, across this asset and
        everything below it. {F.hours(w37?.Unplanned)} unplanned in {w37?.Outages}
        stop{#if F.t(F.ne(w37?.Outages, 1))}s{/if}{#if F.t(w37?.Planned)}, {F.hours(w37?.Planned)} planned{/if}.
        {#if F.t(w37?.Outages)}Mean run between stops {F.hours(w37?.MeanBetween)}.{/if}
      </p>
    {/if}
    <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/downtime"}  class="row wrap">
      <input  name="from"  type="datetime-local"  aria-label="Stopped at">
      <input  name="to"  type="datetime-local"  aria-label="Back at">
      <label  class="row small"><input  type="checkbox"  name="planned"> Planned</label>
      <input  name="note"  placeholder="what stopped it"  aria-label="Note">
      <button  type="submit"  name="action"  value="down"  class="ghost small">Mark down</button>
      <button  type="submit"  name="action"  value="up"  class="ghost small">Back up</button>
      <button  type="submit"  name="action"  value="log"  class="ghost small">Log a past outage</button>
    </form>
    {#if F.t(dot?.Downtime)}
      <ul  class="files"  data-downtime>
        {#each F.rng(dot?.Downtime) as [k38, d38]}
          <li  data-outage={S(d38?.ID)}>
            <strong>{#if F.t(d38?.Open)}down now{:else}{F.hours(d38?.Elapsed)}{/if}</strong>
            <span  class="muted small">{F.date(d38?.StartedAt)}</span>
            {#if F.t(d38?.Planned)}<span  class="tag">planned</span>{:else}<span  class="status blocked">unplanned</span>{/if}
            {#if F.t(d38?.WorkOrder)}<a  href={("/documents/" + S(F.deref_s(d38?.WorkOrderID)))}>{d38?.WorkOrder}</a>{/if}
            {#if F.t(d38?.Note)}<span  class="muted small">{d38?.Note}</span>{/if}
            {#if F.t(d38?.ByName)}<span  class="muted small">{d38?.ByName}</span>{/if}
            <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/downtime/" + S(d38?.ID) + "/delete"}  class="inline">
              <button  type="submit"  class="ghost small">Remove</button>
            </form>
          </li>
        {/each}
      </ul>
    {:else}
      <p  class="muted small">Nothing recorded, which is either very good news or nobody has been asked.</p>
    {/if}

    <h3>Preventive maintenance</h3>
    {#if F.t(dot?.Schedules)}
      <ul  class="files"  data-schedules>
        {#each F.rng(dot?.Schedules) as [k39, d39]}
          <li  data-schedule={S(d39?.ID)}>
            <strong>{d39?.Name}</strong>
            <span  class="muted small">{F.interval(d39)}</span>
            {#if F.t(d39?.Enabled)}
              <span  class={"muted small " + S((F.t(d39?.Overdue) ? ("over") : ""))}>
                due {F.date(d39?.DueAt)}{#if F.t(d39?.Overdue)} · overdue{/if}</span>
            {:else}
              <span  class="tag">paused</span>
            {/if}
            {#if F.t(d39?.Open)}
              
              <a  href={("/documents/" + S(F.deref_s(d39?.Open?.WorkOrderID)))}>open now</a>
            {:else}
              <form  method="post"  action={"/maintenance/" + S(d39?.ID) + "/raise"}  class="inline">
                <input  type="hidden"  name="back"  value={"/documents/" + S(dot?.Doc?.ID)}>
                <button  type="submit"  class="ghost small">Raise now</button>
              </form>
            {/if}
            <form  method="post"  action={"/maintenance/" + S(d39?.ID) + "/enabled"}  class="inline">
              <input  type="hidden"  name="back"  value={"/documents/" + S(dot?.Doc?.ID)}>
              <input  type="hidden"  name="enabled"  value={S((F.t(d39?.Enabled) ? ("off") : ("on")))}>
              <button  type="submit"  class="ghost small">{#if F.t(d39?.Enabled)}Pause{:else}Resume{/if}</button>
            </form>
            <form  method="post"  action={"/maintenance/" + S(d39?.ID) + "/delete"}  class="inline">
              <input  type="hidden"  name="back"  value={"/documents/" + S(dot?.Doc?.ID)}>
              <button  type="submit"  class="ghost small">Remove</button>
            </form>
          </li>
        {/each}
      </ul>
    {:else}
      <p  class="muted small">Nothing scheduled on this asset.</p>
    {/if}

    <form  method="post"  action="/maintenance"  class="row wrap">
      <input  type="hidden"  name="asset"  value={S(dot?.Doc?.ID)}>
      <input  type="hidden"  name="back"  value={"/documents/" + S(dot?.Doc?.ID)}>
      <input  name="name"  placeholder="Quarterly service"  required aria-label="Schedule name">
      <select  name="template"  aria-label="Procedure">
        <option  value="">No procedure</option>
        {#each F.rng(dot?.Templates) as [k40, d40]}<option  value={S(d40?.ID)}>{d40?.Title}</option>{/each}
      </select>
      <input  name="every_days"  type="number"  min="1"  placeholder="days"               style="width:6rem"  aria-label="Every how many days">
      <input  name="meter_key"  placeholder="run_hours"  style="width:8rem"               aria-label="Meter"  list="meter-keys">
      <input  name="every_units"  type="number"  step="any"  min="1"  placeholder="units"               style="width:6rem"  aria-label="Every how many meter units">
      <input  name="lead_days"  type="number"  min="0"  max="365"  value="7"               style="width:5rem"  aria-label="Days of notice">
      <button  type="submit">Schedule</button>
    </form>
    <p  class="muted small">
      Days, meter units, or both. Both means whichever arrives first, because a
      machine that sat idle for a quarter does not need the same service as one
      that ran three shifts.
    </p>

    <h3>Meter readings</h3>
    <datalist  id="meter-keys">
      {#each F.rng(dot?.MeterKeys) as [k41, d41]}<option  value={S(d41)}></option>{/each}
    </datalist>
    <form  method="post"  action={"/documents/" + S(dot?.Doc?.ID) + "/readings"}  class="row">
      <input  name="key"  placeholder="run_hours"  required style="width:8rem"               aria-label="Meter"  list="meter-keys">
      <input  name="value"  type="number"  step="any"  min="0"  required style="width:7rem"               aria-label="Reading">
      <input  name="note"  placeholder="note"  aria-label="Note">
      <button  type="submit">Record</button>
    </form>
    {#if F.t(dot?.Readings)}
      <ul  class="files"  data-readings>
        {#each F.rng(dot?.Readings) as [k42, d42]}
          <li>
            <strong>{d42?.Key}</strong>
            <span  data-reading={S(d42?.Key)}>{F.meter(d42?.Value)}</span>
            <span  class="muted small">{F.date(d42?.ReadAt)}{#if F.t(d42?.ReadName)} · {d42?.ReadName}{/if}
              {#if F.t(d42?.Note)} · {d42?.Note}{/if}</span>
          </li>
        {/each}
      </ul>
    {:else}
      <p  class="muted small">No readings yet.</p>
    {/if}

    {#if F.t(dot?.PMHistory)}
      <h3>Maintenance history</h3>
      <ul  class="files"  data-pm-history>
        {#each F.rng(dot?.PMHistory) as [k43, d43]}
          <li>
            <strong>{d43?.Name}</strong>
            <span  class="muted small">due {F.date(d43?.DueAt)}</span>
            {#if F.t(d43?.CompletedAt)}
              <span  class={"status " + S((F.t(d43?.Late) ? ("blocked") : ("done")))}>
                {#if F.t(d43?.Late)}late{:else}on time{/if}</span>
              <span  class="muted small">{F.date(d43?.CompletedAt)}</span>
            {:else if F.t(d43?.Missed)}
              <span  class="status blocked">missed</span>
            {:else}
              <span  class="status todo">open</span>
            {/if}
            {#if F.t(d43?.WorkOrder)}
              <a  href={("/documents/" + S(F.deref_s(d43?.WorkOrderID)))}>{d43?.WorkOrder}</a>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  {/if}

</article>
{/if}
{/snippet}
{@render content(dot)}
