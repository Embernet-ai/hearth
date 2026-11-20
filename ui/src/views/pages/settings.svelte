<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section>
  <h1>Settings</h1>

  <h2>Personal</h2>
  <div  class="settingsgrid">
    <div  class="card">
      <h3>Account</h3>
      <a  href={("/account")}>Password and sign-in</a>
      <a  href={("/departments/request")}>My departments</a>
    </div>
    <div  class="card">
      <h3>Notifications</h3>
      <a  href={("/inbox#notifications")}>Email, digest and quiet hours</a>
    </div>
    <div  class="card">
      <h3>Tutorial</h3>
      <a  href={("/settings/tutorial")}>Learn Hearth in your own practice company</a>
    </div>
  </div>

  {#if F.t(F.or(dot?.IsAdmin, F.m(dot?.Can, "Has", "people"), F.m(dot?.Can, "Has", "agents"), F.m(dot?.Can, "Has", "audit"), F.m(dot?.Can, "Has", "finance")))}
    <h2>Administration</h2>
    <p  class="muted small">Each group only shows for an account that holds its grant.</p>
    <div  class="settingsgrid">
      {#if F.t(F.or(dot?.IsAdmin, F.m(dot?.Can, "Has", "people")))}
        <div  class="card">
          <h3>People</h3>
          <a  href={("/people")}>People</a>
          <a  href={("/teams")}>Teams</a>
        </div>
      {/if}

      {#if F.t(F.or(dot?.IsAdmin, F.m(dot?.Can, "Has", "agents")))}
        <div  class="card">
          <h3>Automation</h3>
          <a  href={("/rules")}>Rules</a>
          <a  href={("/agents")}>Agents</a>
          <a  href={("/settings/mail")}>Email</a>
          <a  href={("/integrations")}>API</a>
        </div>
      {/if}

      {#if F.t(F.or(dot?.IsAdmin, F.m(dot?.Can, "Has", "audit")))}
        <div  class="card">
          <h3>Oversight</h3>
          <a  href={("/audit")}>Audit</a>
          <a  href={("/ops/jobs")}>Ops</a>
        </div>
      {/if}

      {#if F.t(F.or(dot?.IsAdmin, F.m(dot?.Can, "Has", "finance")))}
        <div  class="card">
          <h3>Billing</h3>
          <a  href={("/reports/time")}>Time report</a>
          <a  href={("/time/imported")}>Imported hours</a>
        </div>
      {/if}
    </div>
  {/if}
</section>
{/snippet}
{@render content(dot)}
