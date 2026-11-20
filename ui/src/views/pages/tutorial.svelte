<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section  class="card tutorial">
  <h1>Tutorial</h1>
  {#if F.t(dot?.Message)}<p  class="ok">{dot?.Message}</p>{/if}

  {#if F.t(dot?.InPlayground)}
    <p>You're in your <strong>playground</strong>. Nothing here touches real work, and nobody else sees it.</p>
    <p>The lessons are tasks in <strong>Tutorial</strong> in the sidebar. Their checklists are the steps.</p>
    <div  class="row">
      <form  method="post"  action="/tutorial/leave"  class="inline">
        <button  type="submit"  class="primary">Back to Hearth</button>
      </form>
      <form  method="post"  action="/tutorial/reset"  class="inline"              data-confirm="Throw your playground away and start again with a fresh one? Everything you did in it goes.">
        <button  type="submit"  class="ghost">Reset my playground</button>
      </form>
    </div>
  {:else}
    <p>Learn Hearth by doing it. Your <strong>playground</strong> is a practice company of your own, full of made-up departments, people and work. Drag tasks around, try every button, break things. Nothing you do there touches real work, and nobody else sees it.</p>
    <ul>
      <li>The lessons are tasks with checklists. Each step says where to find the thing it asks for.</li>
      <li>There are lessons for everyone, and some for managers and admins. Those say so, so you can skip them.</li>
      <li>Your playground stays as you left it until you reset it.</li>
      <li>Some things are switched off there, because they would reach outside Hearth: email, webhooks, tokens, Google and imports.</li>
    </ul>
    <div  class="row">
      <form  method="post"  action="/tutorial/enter"  class="inline">
        <button  type="submit"  class="primary">Enter my playground</button>
      </form>
      <form  method="post"  action="/tutorial/reset"  class="inline"              data-confirm="Throw your playground away? The next time you enter, you get a fresh one.">
        <button  type="submit"  class="ghost">Reset my playground</button>
      </form>
    </div>
    <p  class="muted small">The first visit takes a few seconds while your practice company is built.</p>
  {/if}
</section>
{/snippet}
{@render content(dot)}
