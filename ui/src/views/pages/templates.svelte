<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Templates</h1>
  <p  class="muted small">
    A template is a document with kind template, so writing one is the same act
    as writing the thing it produces. Using it copies the body, the custom
    fields, and the checklist, unticked.
  </p>

  {#if F.t(dot?.Templates)}
    <table  class="list">
      <thead><tr><th>Template</th><th>Creates</th><th>Steps</th><th></th></tr></thead>
      <tbody>
        {#each F.rng(dot?.Templates) as [k1, d1]}
          <tr>
            <td><a  href={("/documents/" + S(d1?.ID))}>{d1?.Title}</a></td>
            <td><span  class="tag">{d1?.Produces}</span></td>
            <td  class="muted">{d1?.Items}</td>
            <td>
              <form  method="post"  action={"/templates/" + S(d1?.ID) + "/use"}  class="row">
                <input  name="title"  placeholder={S(d1?.Title)}>
                <button  type="submit"  class="primary">Use</button>
              </form>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p  class="muted">
      No templates yet. Create a document with kind template, give it a body and
      a checklist, and set its Creates field to what it should produce.
    </p>
  {/if}

  <form  method="post"  action="/documents"  class="row">
    <input  type="hidden"  name="kind"  value="template">
    <input  name="title"  placeholder="New template name"  required>
    <button  type="submit">Create a template</button>
  </form>
</section>
{/snippet}
{@render content(dot)}
