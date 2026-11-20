<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section  class="card centered">
  <h1>Choose your password</h1>
  {#if F.t(dot?.Done)}
    <p>Your password is set. You can sign in now.</p>
    <p><a  class="button primary"  href={("/login")}>Sign in</a></p>
  {:else}
    {#if F.t(dot?.Message)}<p  class="err">{dot?.Message}</p>{/if}
    {#if F.t(dot?.Token)}
      <p  class="muted">For {dot?.LinkEmail}.</p>
      <form  method="post"  action="/set-password"  class="stack">
        <input  type="hidden"  name="t"  value={S(dot?.Token)}>
        <label  for="password">New password</label>
        <input  id="password"  name="password"  type="password"  required minlength="12"  autofocus autocomplete="new-password">
        <label  for="again">New password again</label>
        <input  id="again"  name="again"  type="password"  required minlength="12"  autocomplete="new-password">
        <p  class="muted small">Twelve characters at least. A few ordinary words strung together beat a symbol rule.</p>
        <button  type="submit"  class="primary">Set password</button>
      </form>
    {:else}
      <p  class="muted">Ask whoever looks after Hearth for a new link.</p>
    {/if}
  {/if}
</section>
{/snippet}
{@render content(dot)}
