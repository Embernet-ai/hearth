<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<h1>{dot?.Heading}</h1>
<p  class="muted">
  What you did away from a signal, and anything that needs you to decide. This
  page works with no connection, and so does everything on it.
</p>

<p  class="err"  data-signin-needed hidden>
  Your session has run out, so nothing can be sent yet. Sign in again and it
  goes on its own.
</p>


<div  class="outgrid">
<section  class="outcol">
<h2>On this phone</h2>
<ul  class="files touchlist"  data-queue></ul>
<p  class="muted small"  data-queue-empty>
  Nothing is waiting. Everything you have done has gone in.
</p>


</section>

<section  class="outcol">
<h2>Waiting on you</h2>
{#if F.t(dot?.Outbox)}
  <ul  class="files touchlist"  data-undecided>
    {#each F.rng(dot?.Outbox) as [k1, d1]}
      <li  data-op={S(d1?.ID)}>
        <strong>{d1?.Kind}</strong>
        {#if F.t(d1?.EntityID)}{@const v_title = F.index(dot?.OutboxTitles, F.deref_s(d1?.EntityID))}
          <a  href={("/documents/" + S(F.deref_s(d1?.EntityID)))}>
            
            {#if F.t(v_title)}{v_title}{:else}the job{/if}
          </a>
        {/if}
        <span  class="status blocked">
          {#if F.t(F.eq(d1?.Status, "conflict"))}changed while you were out{:else}unknown{/if}
        </span>
        <span  class="muted small"  data-queued-at>
          done {F.ago(d1?.QueuedAt)}{#if F.t(d1?.Late)}, arrived {F.ago(d1?.ReceivedAt)}{/if}
        </span>
        {#if F.t(d1?.Message)}<span  class="muted small">{d1?.Message}</span>{/if}

        {#if F.t(F.eq(d1?.Status, "conflict"))}
          
          <form  method="post"  action={"/offline/" + S(d1?.ID) + "/keep"}  class="inline">
            <button  type="submit"  class="primary">Mine is right, apply it</button>
          </form>
        {:else}
          <span  class="muted small">
            Whether this landed cannot be answered from here. Open the job and
            look before you decide.
          </span>
          {#if F.t(d1?.Body)}
            <form  method="post"  action={"/offline/" + S(d1?.ID) + "/keep"}  class="inline">
              <button  type="submit">It did not land, apply it</button>
            </form>
          {/if}
        {/if}
        <form  method="post"  action={"/offline/" + S(d1?.ID) + "/discard"}  class="inline">
          <button  type="submit"  class="ghost small">Throw it away</button>
        </form>
      </li>
    {/each}
  </ul>
{:else}
  <p  class="muted small">Nothing needs a decision.</p>
{/if}

</section>

{#if F.t(dot?.OutboxRecent)}
<section  class="outcol">
  <h2>Lately</h2>
  <p  class="muted small">
    What has come off a phone recently, so the question "did my morning
    actually go in" has an answer.
  </p>
  <ul  class="files touchlist"  data-recent>
    {#each F.rng(dot?.OutboxRecent) as [k2, d2]}
      <li  data-recent-op={S(d2?.ID)}>
        <strong>{d2?.Kind}</strong>
        {#if F.t(d2?.EntityID)}{@const v_title = F.index(dot?.OutboxTitles, F.deref_s(d2?.EntityID))}
          <a  href={("/documents/" + S(F.deref_s(d2?.EntityID)))}>
            
            {#if F.t(v_title)}{v_title}{:else}the job{/if}
          </a>
        {/if}
        <span  class={"status " + S((F.t(F.eq(d2?.Status, "applied")) ? ("done") : (F.t(F.eq(d2?.Status, "resolved")) ? ("done") : (F.t(F.eq(d2?.Status, "discarded")) ? ("cancelled") : ("blocked")))))}                data-op-status={S(d2?.Status)}>{d2?.Status}</span>
        <span  class="muted small">{F.ago(d2?.ReceivedAt)}</span>
        {#if F.t(d2?.Late)}
          <span  class="muted small"  data-waited>
            held for {F.duration(d2?.Waited)} with no signal
          </span>
        {/if}
      </li>
    {/each}
  </ul>
</section>
{/if}
</div>
{/snippet}
{@render content(dot)}
