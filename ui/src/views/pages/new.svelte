<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<form  class="card stack"  method="post"  action="/documents">
  <h2>New {#if F.t(dot?.KindChoice)}item{:else}{dot?.KindLabel}{/if}</h2>
  {#if F.t(dot?.KindChoice)}
    <label  for="kind">What is it?</label>
    <select  id="kind"  name="kind">
      <option  value="task"  selected>Task</option>
      <option  value="doc">Document</option>
    </select>
  {:else}
    <input  type="hidden"  name="kind"  value={S(dot?.Kind)}>
  {/if}
  {#if F.t(dot?.SpaceID)}<input  type="hidden"  name="space"  value={S(dot?.SpaceID)}>{/if}
  <label  for="title">Title</label>
  <input  id="title"  name="title"  required autofocus>
  <label  for="body">Details</label>
  <textarea  id="body"  name="body"  rows="6"></textarea>

  
  {#if F.t(dot?.HasTask)}
    <div  class="row taskfields">
      <span  class="stack">
        <label  for="priority">Priority</label>
        <select  id="priority"  name="priority">
          <option  value="0">low</option>
          <option  value="1"  selected>normal</option>
          <option  value="2">high</option>
          <option  value="3">urgent</option>
        </select>
      </span>
      <span  class="stack">
        <label  for="due">Due</label>
        <input  id="due"  type="date"  name="due">
      </span>
      <span  class="stack">
        <label  for="assignee">Assignee</label>
        <select  id="assignee"  name="assignee">
          <option  value="">Nobody</option>
          {#each F.rng(dot?.People) as [k1, d1]}<option  value={S(d1?.ID)}>{d1?.Name}</option>{/each}
        </select>
      </span>
    </div>
  {/if}

  <div  class="row">
    <button  type="submit"  class="primary">Create</button>
    <button  type="button"  class="ghost"  popovertarget="newpanel"  popovertargetaction="hide">Cancel</button>
  </div>
</form>
{/snippet}
{@render content(dot)}
