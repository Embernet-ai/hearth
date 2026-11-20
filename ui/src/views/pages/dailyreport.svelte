<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>File today's report</h1>
  <p  class="muted small">
    {dot?.RepDay}. One report a day, with whatever you have: what you did, photos,
    and a note for the people reading it. Your report is yours and the people
    who run the department you file it under.
  </p>

  {#if F.t(dot?.RepError)}<p  class="card small"  role="alert">{dot?.RepError}</p>{/if}

  <form  method="post"  action="/reports/daily"  enctype="multipart/form-data"  class="stack">
    <label  for="rep-space">Department</label>
    <select  id="rep-space"  name="space"  required>
      {#each F.rng(dot?.RepTree) as [k1, d1]}
        <option  value={S(d1?.ID)}  {...(F.t(F.eq(d1?.ID, dot?.RepSpace)) ? {"selected": true} : {})}>{d1?.Path}</option>
      {/each}
    </select>

    <label  for="rep-body">What you did today
      <span  class="help"  tabindex="0"              title="Formatting: **bold**, *italic*, and a line starting with - for a list.">?</span>
    </label>
    <textarea  id="rep-body"  name="body"  rows="9"  placeholder="**Done:** &#10;&#10;**Doing:** &#10;&#10;**Blocked:** ">{dot?.RepBody}</textarea>

    <label  for="rep-camera">Take a photo</label>
    
    <input  id="rep-camera"  type="file"  name="camera"  accept="image/*"  capture="environment">

    <label  for="rep-photos">Add photos from your phone or computer</label>
    <input  id="rep-photos"  type="file"  name="photos"  accept="image/*"  multiple>

    <label  for="rep-files">Attach other files</label>
    <input  id="rep-files"  type="file"  name="files"  multiple>

    <label  for="rep-comment">A comment for the reader</label>
    <textarea  id="rep-comment"  name="comment"  rows="3">{dot?.RepComment}</textarea>

    <div  class="row">
      <button  type="submit"  class="primary big">File report</button>
      <a  class="ghost"  href={("/reports/daily")}>Cancel</a>
    </div>
  </form>
</section>
{/snippet}
{@render content(dot)}
