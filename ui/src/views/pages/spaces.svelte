<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Spaces &amp; folders</h1>
  <p  class="muted small">
    Departments, and the folders inside them, are where work is filed. Open one
    to see its work; use <strong>+</strong> to add a folder inside it. A space
    you cannot see is not shown.
  </p>

  {#if F.t(dot?.Message)}<p  class="card small">{dot?.Message}</p>{/if}

  {#if F.t(dot?.CanAdminSpaces)}
    <p><a  class="newdept"  data-addunder=""  href={("/spaces?parent=")}>+ New department</a></p>
  {/if}

  {#if F.t(dot?.Spaces)}
    <ul  class="spacetree">
      {#each F.rng(dot?.Spaces) as [k1, d1]}{@const v_can = F.index(dot?.ManageableSpaces, d1?.ID)}
        
        <li  data-space={S(d1?.ID)}  data-parent={S((((w2) => F.t(w2) ? (S(w2)) : "")(d1?.ParentID)))}  data-depth={S(d1?.Depth)} {...(F.t(v_can) ? {"draggable": "true"} : {})}  style={"padding-left:" + S(F.indent(d1?.Depth))}>
          <div  class="spacerow">
            {#if F.t(v_can)}<span  class="draghandle"  title="Drag to reorder"  aria-hidden="true">⠿</span>{/if}
            {#if F.t(F.index(dot?.SpaceHasChildren, d1?.ID))}
              <button  type="button"  class="treetoggle"  aria-expanded="true"  aria-label="Collapse">▾</button>
            {:else}<span  class="treespacer"></span>{/if}
            <span  class={"sname " + S((F.t(F.eq(d1?.Depth, 0)) ? ("dept") : ""))}>{d1?.Name}</span>
            <span  class="muted small">{d1?.Documents} document{#if F.t(F.ne(d1?.Documents, 1))}s{/if}</span>
            {#if F.t(v_can)}{@const v_me = d1?.ID}
              <a  class="addfolder shown"  data-addunder={S(d1?.ID)}  href={("/spaces?parent=" + S(d1?.ID))}  title={"Add a folder in " + S(d1?.Name)}  aria-label="Add a folder">+</a>
              <a  class="small"  href={"/spaces/" + S(d1?.ID) + "/access"}  title={"Give somebody " + S(d1?.Name) + " and what is in it"}>Access</a>
              <details  class="rowedit">
                <summary  class="ghost small">Edit</summary>
                <div  class="editbody">
                  <form  method="post"  action={"/spaces/" + S(d1?.ID) + "/rename"}  class="row">
                    <input  name="name"  value={S(d1?.Name)}  aria-label="Name"  style="width:12rem">
                    <button  type="submit"  class="ghost small">Rename</button>
                  </form>
                  <form  method="post"  action={"/spaces/" + S(d1?.ID) + "/move"}  class="row">
                    <select  name="parent"  aria-label="Move into">
                      <option  value="">Top level</option>
                      
                      {#each F.rng(dot?.Spaces) as [k3, d3]}{#if F.t(F.ne(d3?.ID, v_me))}<option  value={S(d3?.ID)}>{d3?.Path}</option>{/if}{/each}
                    </select>
                    <button  type="submit"  class="ghost small">Move</button>
                  </form>
                  <form  method="post"  action={"/spaces/" + S(d1?.ID) + "/delete"}  class="inline"                          data-confirm={"Remove " + S(d1?.Name) + "? It must be empty first."}>
                    <button  type="submit"  class="ghost small danger">Remove</button>
                  </form>
                </div>
              </details>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
  {:else}
    <p  class="muted">No spaces yet.</p>
  {/if}

  {#if F.t(dot?.CanManageSpaces)}
    
    <div  id="spaceorderbar"  class="orderbar"  hidden>
      <span>You rearranged things. Save the new order?</span>
      <button  type="button"  id="spaceorder-save"  class="primary small">Save order</button>
      <button  type="button"  id="spaceorder-cancel"  class="ghost small">Cancel</button>
    </div>
  {/if}

  {#if F.t(dot?.CanManageSpaces)}
    <p  class="muted small">
      <strong>+ New department</strong> makes a top-level department (admins
      only). <strong>+</strong> on a department makes a folder inside it, which
      its manager can do. Drag by the handle to reorder. A space has to be empty
      before it can be removed.
    </p>
  {:else}
    <p  class="muted small">Only a manager of a department or an admin can add or change folders here.</p>
  {/if}
</section>
{/snippet}
{@render content(dot)}
