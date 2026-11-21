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

    {#if cloud.SaveFile}
      <form  method="post"  action="/cloud/save"  class="card row"  data-save>
        <input  type="hidden"  name="provider"  value={cloud.Provider}>
        <input  type="hidden"  name="drive"  value={cloud.DriveID}>
        <input  type="hidden"  name="folder"  value={cloud.FolderID}>
        <input  type="hidden"  name="savedoc"  value={cloud.SaveDoc}>
        <input  type="hidden"  name="savefile"  value={cloud.SaveFile}>
        <span>Save <strong>{cloud.SaveName}</strong> into this folder.</span>
        <button  type="submit"  class="primary small">Save here</button>
      </form>
    {/if}

    <div  class="row">
      <form  method="post"  action="/cloud/upload"  enctype="multipart/form-data"  class="inline row"  data-upload>
        <input  type="hidden"  name="provider"  value={cloud.Provider}>
        <input  type="hidden"  name="drive"  value={cloud.DriveID}>
        <input  type="hidden"  name="folder"  value={cloud.FolderID}>
        <input  type="file"  name="file"  aria-label="File to upload">
        <button  type="submit"  class="small">Upload here</button>
      </form>
      <form  method="post"  action="/cloud/mkdir"  class="inline row"  data-mkdir>
        <input  type="hidden"  name="provider"  value={cloud.Provider}>
        <input  type="hidden"  name="drive"  value={cloud.DriveID}>
        <input  type="hidden"  name="folder"  value={cloud.FolderID}>
        <input  type="text"  name="name"  placeholder="New folder"  aria-label="New folder name">
        <button  type="submit"  class="ghost small">Make folder</button>
      </form>
    </div>

    {#if cloud.Items && cloud.Items.length}
      <ul  class="list">
        {#each cloud.Items as it}
          <li  data-item={it.ID}>
            <div  class="row">
              {#if it.Folder}
                <a  href={"/cloud?provider=" + cloud.Provider + "&drive=" + encodeURIComponent(cloud.DriveID) + "&folder=" + encodeURIComponent(it.ID) + (cloud.DocID ? "&doc=" + cloud.DocID : "")}>{it.Name}/</a>
              {:else}
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
              {/if}
            </div>
            <details  class="rowedit">
              <summary  class="ghost small">Rename, move, replace or delete</summary>
              <div  class="editbody">
                <form  method="post"  action="/cloud/rename"  class="row">
                  <input  type="hidden"  name="provider"  value={cloud.Provider}>
                  <input  type="hidden"  name="drive"  value={cloud.DriveID}>
                  <input  type="hidden"  name="folder"  value={cloud.FolderID}>
                  <input  type="hidden"  name="item"  value={it.ID}>
                  <input  type="text"  name="name"  value={it.Name}  aria-label={"New name for " + it.Name}>
                  <button  type="submit"  class="small">Rename</button>
                </form>
                <form  method="post"  action="/cloud/move"  class="row">
                  <input  type="hidden"  name="provider"  value={cloud.Provider}>
                  <input  type="hidden"  name="drive"  value={cloud.DriveID}>
                  <input  type="hidden"  name="folder"  value={cloud.FolderID}>
                  <input  type="hidden"  name="item"  value={it.ID}>
                  <select  name="to"  aria-label={"Move " + it.Name + " to"}>
                    <option  value="">The top of the drive</option>
                    {#each (cloud.Folders ?? []) as fo}{#if fo.ID !== it.ID}<option  value={fo.ID}>{fo.Name}</option>{/if}{/each}
                  </select>
                  <button  type="submit"  class="small">Move</button>
                </form>
                {#if !it.Folder}
                  <form  method="post"  action="/cloud/replace"  enctype="multipart/form-data"  class="row">
                    <input  type="hidden"  name="provider"  value={cloud.Provider}>
                    <input  type="hidden"  name="drive"  value={cloud.DriveID}>
                    <input  type="hidden"  name="folder"  value={cloud.FolderID}>
                    <input  type="hidden"  name="item"  value={it.ID}>
                    <input  type="file"  name="file"  aria-label={"New content for " + it.Name}>
                    <button  type="submit"  class="small">Replace the content</button>
                  </form>
                {/if}
                <form  method="post"  action="/cloud/delete"  class="row"  data-confirm={"Move " + it.Name + " to the bin?"}>
                  <input  type="hidden"  name="provider"  value={cloud.Provider}>
                  <input  type="hidden"  name="drive"  value={cloud.DriveID}>
                  <input  type="hidden"  name="folder"  value={cloud.FolderID}>
                  <input  type="hidden"  name="item"  value={it.ID}>
                  <button  type="submit"  class="ghost small">Move to the bin</button>
                </form>
              </div>
            </details>
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
