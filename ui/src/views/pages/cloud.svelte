<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
  const cloud = $derived(dot?.Cloud);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Cloud files</h1>
  {#if F.t(cloud?.Notice)}<p  class="card small"  data-flash>{cloud?.Notice}</p>{/if}

  {#if !cloud?.Providers || cloud.Providers.length === 0}
    <p  class="muted">
      Nothing to browse yet. An administrator turns on Files for Microsoft 365 or Google Workspace, and you connect
      your account and tick Files, under <a  href="/settings/connectors">Settings, Connectors</a>.
    </p>
  {:else}
    {#if cloud.DocID}
      <p  class="muted small">Adding to <a  href={"/documents/" + cloud.DocID}>{cloud.DocTitle}</a>.</p>
    {/if}

    <form  method="get"  action="/cloud"  class="row">
      {#if cloud.DocID}<input  type="hidden"  name="doc"  value={cloud.DocID}>{/if}
      <select  name="provider"  aria-label="Provider">
        {#each cloud.Providers as p}<option  value={p.Key}  selected={p.Key === cloud.Provider}>{p.Label}</option>{/each}
      </select>
      <select  name="drive"  aria-label="Drive">
        {#each (cloud.Drives ?? []) as d}<option  value={d.ID}  selected={d.ID === cloud.DriveID}>{d.Name}</option>{/each}
      </select>
      <button  type="submit"  class="ghost small">Open</button>
    </form>

    {#if cloud.FolderID}
      <p><a  href={"/cloud?provider=" + cloud.Provider + "&drive=" + encodeURIComponent(cloud.DriveID) + (cloud.DocID ? "&doc=" + cloud.DocID : "")}>Back to the top</a></p>
    {/if}

    {#if cloud.Items && cloud.Items.length}
      <ul  class="list">
        {#each cloud.Items as it}
          <li  data-item={it.ID}>
            {#if it.Folder}
              <a  href={"/cloud?provider=" + cloud.Provider + "&drive=" + encodeURIComponent(cloud.DriveID) + "&folder=" + encodeURIComponent(it.ID) + (cloud.DocID ? "&doc=" + cloud.DocID : "")}>{it.Name}/</a>
            {:else}
              <div  class="row">
                <a  href={it.WebURL}  target="_blank"  rel="noopener">{it.Name}</a>
                <form  method="post"  action="/cloud/link"  class="inline row">
                  <input  type="hidden"  name="provider"  value={cloud.Provider}>
                  <input  type="hidden"  name="drive"  value={cloud.DriveID}>
                  <input  type="hidden"  name="item"  value={it.ID}>
                  {#if cloud.DocID}
                    <input  type="hidden"  name="doc"  value={cloud.DocID}>
                  {:else}
                    <select  name="doc"  aria-label="Document">
                      {#each (cloud.Documents ?? []) as d}<option  value={d.ID}>{d.Title}</option>{/each}
                    </select>
                  {/if}
                  <button  type="submit"  class="ghost small">Link</button>
                  <button  type="submit"  class="small"  formaction="/cloud/import">Import a copy</button>
                </form>
              </div>
            {/if}
          </li>
        {/each}
      </ul>
    {:else}
      <p  class="muted">This folder is empty.</p>
    {/if}
  {/if}
</section>
{/snippet}
{@render content(dot)}
