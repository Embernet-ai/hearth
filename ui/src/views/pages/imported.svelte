<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Imported hours</h1>
  <p  class="muted small">
    ClickUp's export says how long a task took, not who spent the time, so the
    importer put every task's hours under whoever ran the import. The one clue
    left is who the task is assigned to. Moving hours here does not approve
    them: they stay pending, in the new person's approver's queue.
  </p>

  {#if F.t(dot?.Flash)}<p  class="card small">{dot?.Flash}</p>{/if}

  {#if F.t(F.not(dot?.ImportedCount))}
    <p  class="muted">No imported hours are waiting on a decision.</p>
  {:else}
    <p>
      <strong>{dot?.ImportedCount}</strong> imported entries still pending,
      <strong>{F.hours(dot?.ImportedMinutes)}</strong> in total.
      {#if F.t(dot?.ImportedSettled)}<span  class="muted small">{dot?.ImportedSettled} are already on the person their task names.</span>{/if}
    </p>

    <h2>Clear matches</h2>
    {#if F.t(dot?.ImportedByPerson)}
      <p  class="muted small">
        Each of these tasks is assigned to exactly one person, so that is who the
        hours go to.
      </p>
      <form  method="post"  action="/time/imported/apply"  class="row"              data-confirm="Move every clear match onto its assignee? They stay pending for approval.">
        <button  type="submit">Move all clear matches</button>
      </form>
      <table  class="list">
        <thead><tr><th>Goes to</th><th>Entries</th><th>Hours</th><th></th></tr></thead>
        <tbody>
          {#each F.rng(dot?.ImportedByPerson) as [k1, d1]}
            <tr>
              <td>{d1?.PersonName}</td>
              <td>{d1?.Count}</td>
              <td>{F.hours(d1?.Minutes)}</td>
              <td>
                <form  method="post"  action="/time/imported/apply"  class="inline"                        data-confirm={"Move " + S(d1?.Count) + " entries (" + S(F.hours(d1?.Minutes)) + ") to " + S(d1?.PersonName) + "?"}>
                  <input  type="hidden"  name="person"  value={S(d1?.PersonID)}>
                  <button  type="submit"  class="ghost small">Move these</button>
                </form>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p  class="muted">None left.</p>
    {/if}

    <h2>Needs a person</h2>
    {#if F.t(dot?.ImportedManual)}{@const v_people = dot?.People}
      <p  class="muted small">
        These tasks name several people or nobody, so there is no way to know
        whose time it was. Pick who it belongs to.
      </p>
      <table  class="list">
        <thead><tr><th>Task</th><th>Day</th><th>Hours</th><th>Assigned to</th><th>Currently on</th><th></th></tr></thead>
        <tbody>
          
          {#each F.rng(dot?.ImportedManual) as [k2, d2]}
            <tr>
              <td><a  href={("/documents/" + S(d2?.DocumentID))}>{#if F.t(d2?.DocTitle)}{d2?.DocTitle}{:else}a task{/if}</a></td>
              <td>{F.date(d2?.StartedAt)}</td>
              <td>{F.hours(d2?.Minutes)}</td>
              <td  class="muted small">{#if F.t(d2?.Assignees)}{#each F.rng(d2?.Assignees) as [k3, d3]}{@const v_i = k3}{@const v_a = d3}{#if F.t(v_i)}, {/if}{v_a}{/each}{:else}nobody{/if}</td>
              <td  class="muted small">{d2?.PersonName}</td>
              <td>
                <form  method="post"  action={"/time/imported/" + S(d2?.ID)}  class="row">
                  <select  name="to"  aria-label="Move to">
                    <option  value="">Move to…</option>
                    {#each F.rng(v_people) as [k4, d4]}<option  value={S(d4?.ID)}>{d4?.Name}</option>{/each}
                  </select>
                  <button  type="submit"  class="ghost small">Move</button>
                </form>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p  class="muted">None.</p>
    {/if}
  {/if}
</section>
{/snippet}
{@render content(dot)}
