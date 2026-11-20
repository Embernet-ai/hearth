<script lang="ts">
  // @ts-nocheck
  import { F, S, A } from '../../lib/gofuncs';
  let { pg }: { pg: any; onEnv?: any } = $props();
  const dot: any = $derived(pg);
</script>

{#snippet content(dot: any)}
<section  class="card centered">
  <h1>Set up Hearth</h1>
  <p  class="muted">Nobody has claimed this installation yet. Create the owner account.</p>
  {#if F.t(dot?.Message)}<p  class="err">{dot?.Message}</p>{/if}
  <form  method="post"  action="/setup"  class="stack">
    <label  for="org">Organisation</label>
    <input  id="org"  name="org"  required autofocus placeholder="Fireball Industries">
    <label  for="name">Your name</label>
    <input  id="name"  name="name"  required placeholder="Pat Morgan">
    <label  for="email">Email</label>
    <input  id="email"  name="email"  type="email"  required placeholder="you@example.com">
    <label  for="password">Password</label>
    <input  id="password"  name="password"  type="password"  required minlength="12"             autocomplete="new-password">
    <p  class="muted small">At least 12 characters. Length beats punctuation.</p>
    <button  type="submit"  class="primary">Create owner account</button>
  </form>
</section>
{/snippet}
{@render content(dot)}
