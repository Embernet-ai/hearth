<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section  class="settingspage">
  <h1>Settings</h1>

  <div  class="settingsgroup personal">
  <h2>Personal</h2>
  <div  class="settingsgrid">
    <div  class="card">
      <h3>Account</h3>
      <a  class="settinglink"  href={("/account")}><span  class="sl-title">Password and sign-in</span><span  class="sl-note">Your password and your picture.</span></a>
      <a  class="settinglink"  href={("/departments/request")}><span  class="sl-title">My departments</span><span  class="sl-note">Which departments you belong in. They decide who approves your hours.</span></a>
    </div>
    {#if F.t(dot?.ConnectorsOn)}
    <div  class="card">
      <h3>Connections</h3>
      <a  class="settinglink"  href={("/settings/connectors")}><span  class="sl-title">Microsoft 365 and Google Workspace</span><span  class="sl-note">Connect your own account for calendar, contacts, mail and files.</span></a>
      <a  class="settinglink"  href={("/mailbox")}><span  class="sl-title">Mailbox</span><span  class="sl-note">Read, send, reply, forward and file your Microsoft 365 or Google mail.</span></a>
      <a  class="settinglink"  href={("/cloud")}><span  class="sl-title">Cloud files</span><span  class="sl-note">Open, upload, edit, move and delete files in OneDrive, SharePoint and Drive.</span></a>
    </div>
    {/if}
    <div  class="card">
      <h3>Notifications</h3>
      <a  class="settinglink"  href={("/inbox#notifications")}><span  class="sl-title">Email, digest and quiet hours</span><span  class="sl-note">How often email reaches you, and when it stays quiet.</span></a>
    </div>
    <div  class="card">
      <h3>Tutorial</h3>
      <a  class="settinglink"  href={("/settings/tutorial")}><span  class="sl-title">Learn Hearth in your own practice company</span><span  class="sl-note">Practice with nothing real at stake.</span></a>
    </div>
  </div>
  </div>

  {#if F.t(F.or(dot?.IsAdmin, F.m(dot?.Can, "Has", "people"), F.m(dot?.Can, "Has", "agents"), F.m(dot?.Can, "Has", "audit"), F.m(dot?.Can, "Has", "finance")))}
    <div  class="settingsgroup admin">
    <h2>Administration</h2>
    <p  class="muted small">Each group only shows for an account that holds its grant.</p>
    <div  class="settingsgrid">
      {#if F.t(F.or(dot?.IsAdmin, F.m(dot?.Can, "Has", "people")))}
        <div  class="card">
          <h3>People</h3>
          <a  class="settinglink"  href={("/people")}><span  class="sl-title">People</span><span  class="sl-note">Everyone with an account, and what each can reach.</span></a>
          <a  class="settinglink"  href={("/teams")}><span  class="sl-title">Teams</span><span  class="sl-note">Groups you can share work with as one.</span></a>
        </div>
      {/if}

      {#if F.t(F.or(dot?.IsAdmin, F.m(dot?.Can, "Has", "agents")))}
        <div  class="card">
          <h3>Automation</h3>
          <a  class="settinglink"  href={("/rules")}><span  class="sl-title">Rules</span><span  class="sl-note">When this happens, do that.</span></a>
          <a  class="settinglink"  href={("/agents")}><span  class="sl-title">Agents</span><span  class="sl-note">Accounts for automation, with only the grants they were given.</span></a>
          <a  class="settinglink"  href={("/settings/mail")}><span  class="sl-title">Email</span><span  class="sl-note">How notifications leave Hearth by email.</span></a>
          <a  class="settinglink"  href={("/integrations")}><span  class="sl-title">API</span><span  class="sl-note">Tokens that let other systems act as a person.</span></a>
          <a  class="settinglink"  href={("/settings/connectors")}><span  class="sl-title">Connectors</span><span  class="sl-note">Microsoft 365 and Google Workspace: which are on, and who is connected.</span></a>
        </div>
      {/if}

      {#if F.t(F.or(dot?.IsAdmin, F.m(dot?.Can, "Has", "audit")))}
        <div  class="card">
          <h3>Oversight</h3>
          <a  class="settinglink"  href={("/audit")}><span  class="sl-title">Audit</span><span  class="sl-note">Permission changes, deletions and exports, with who did them.</span></a>
          <a  class="settinglink"  href={("/ops/jobs")}><span  class="sl-title">Ops</span><span  class="sl-note">Background work, and where it stands.</span></a>
        </div>
      {/if}

      {#if F.t(F.or(dot?.IsAdmin, F.m(dot?.Can, "Has", "finance")))}
        <div  class="card">
          <h3>Billing</h3>
          <a  class="settinglink"  href={("/reports/time")}><span  class="sl-title">Time report</span><span  class="sl-note">Hours by person for a period.</span></a>
          <a  class="settinglink"  href={("/time/imported")}><span  class="sl-title">Imported hours</span><span  class="sl-note">Hours from the ClickUp import, waiting to move to the right person.</span></a>
        </div>
      {/if}
    </div>
    </div>
  {/if}
</section>
{/snippet}
{@render content(dot)}
