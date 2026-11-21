<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  import FilePicker from '../../components/FilePicker.svelte';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
  const res = $derived(dot?.CRMImport);
  const label = { name: 'Name', domain: 'Website', kind: 'Kind', notes: 'Notes', email: 'Email', phone: 'Phone', title: 'Title', company: 'Company', stage: 'Stage', amount: 'Amount', currency: 'Currency', close_date: 'Close date', contact_email: 'Contact email', owner_email: 'Owner email' };
  const noun = { companies: 'companies', contacts: 'contacts', deals: 'deals' };
</script>

{#snippet content(dot: any)}
<section  class="card stack">
  <div  class="row spread">
    <h1>Import from a CSV</h1>
    <span  class="row">
      <a  class="chip"  href={("/companies")}>Companies</a>
      <a  class="chip"  href={("/contacts")}>Contacts</a>
      {#if F.t(dot?.SalesOn)}<a  class="chip"  href={("/deals")}>Deals</a>{/if}
    </span>
  </div>

  {#if F.t(res)}
    <div  class="notice"  data-crm-import-result>
      <p>
        <strong>{res.Check ? 'Checked, nothing was created.' : 'Imported.'}</strong>
        <span  data-created={S(res.Created)}><strong>{res.Created}</strong> {res.Check ? 'would be created' : 'created'}</span> ·
        <span  data-skipped={S(res.Skipped)}><strong>{res.Skipped}</strong> already there</span> ·
        <span  data-failed={S(res.Failed)}><strong>{res.Failed}</strong> failed</span>
        <span  class="muted">of {res.Total} in the file</span>
        {#if res.CompaniesCreated}<span  class="muted">· {res.CompaniesCreated} {res.Check ? 'new companies would be made' : 'new companies made'} for the names in the file</span>{/if}
      </p>
    </div>
    {#if F.t(res.Truncated)}
      <p  class="warn">The file has more than {res.MaxRows} records. The first {res.MaxRows} were read. Run the rest as a second file.</p>
    {/if}
    {#if F.t(res.Used)}
      <p  class="small"><strong>Columns read:</strong>
        {#each F.rng(res.Used) as [k1, d1]}<span  class="tag">{d1.Header} &rarr; {label[d1.Field] ?? d1.Field}</span> {/each}
      </p>
    {/if}
    {#if F.t(res.Ignored)}
      <p  class="small muted"><strong>Columns left out, Hearth has no place for them:</strong> {F.rng(res.Ignored).map(([k2, d2]) => d2).join(', ')}</p>
    {/if}
    {#if F.t(res.Problems)}
      <ul  class="problems">
        {#each F.rng(res.Problems) as [k3, d3]}<li  class="small">{d3}</li>{/each}
        {#if res.MoreProblems}<li  class="small muted">and {res.MoreProblems} more</li>{/if}
      </ul>
    {/if}
    {#if !res.Check}
      <p><a  href={('/' + noun[res.Kind])}>See what came in</a></p>
    {/if}
  {/if}

  {#if F.t(dot?.Message)}<p  class="error">{dot?.Message}</p>{/if}

  <form  method="post"  action="/crm/import"  enctype="multipart/form-data"  class="fields">
    <label  for="kind">The file holds</label>
    <select  id="kind"  name="kind">
      <option  value="companies"  selected={res?.Kind === 'companies'}>Companies</option>
      <option  value="contacts"  selected={res?.Kind === 'contacts'}>Contacts</option>
      {#if F.t(dot?.SalesOn)}<option  value="deals"  selected={res?.Kind === 'deals'}>Deals</option>{/if}
    </select>
    <label  for="file">CSV file</label>
    <FilePicker  id="file"  name="file"  accept=".csv,.tsv,text/csv"  required  kind="CSV file" />
    <div  class="row">
      <button  type="submit"  name="mode"  value="check"  class="primary">Check the file</button>
      <button  type="submit"  name="mode"  value="import">Import it</button>
    </div>
  </form>

  <h3>How it reads the file</h3>
  <ul  class="small muted">
    <li>Columns are matched by their header name, in any order, with a comma, semicolon or tab between them. Check the file first: it lists the columns it read and the ones it left out, and creates nothing.</li>
    <li><strong>Companies:</strong> Name, Website or Domain, Kind, Notes. A company already in Hearth with the same website or name is skipped.</li>
    <li><strong>Contacts:</strong> Name (or First and Last name), Email, Phone, Title, Company, Notes. A contact whose email is already in Hearth is skipped. A company name that is new is created.</li>
    <li><strong>Deals:</strong> Name, Stage (New, Screening, Meeting, Proposal, Customer, Won, Lost, No response), Amount, Currency, Close date, Company, Contact email, Owner email, Notes. A deal with the same name at the same company is skipped.</li>
    <li>A row it cannot read fails on its own, with the line number and the reason, and the rest still go in. Running the same file again changes nothing that is already there.</li>
    <li>Up to 5000 records in one file.</li>
  </ul>
</section>
{/snippet}
{@render content(dot)}
