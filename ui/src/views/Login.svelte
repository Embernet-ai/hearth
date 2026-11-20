<script lang="ts">
  import type { Envelope } from '../lib/types';
  import { post } from '../lib/api';

  let { env, onEnv }: { env: Envelope; onEnv: (e: Envelope, url: string) => void } = $props();
  const pg = $derived(env.page);
  let busy = $state(false);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    try {
      // The sign-in form posts to /login with email and password.
      const r = await post('/login', new FormData(e.currentTarget as HTMLFormElement));
      onEnv(r.env, r.url);
    } finally {
      busy = false;
    }
  }
</script>

<main class="centered-page">
  <section class="card centered">
    <h1>Hearth</h1>
    <p class="muted">An all-in-one workspace. One binary, your hardware, no tiers.</p>
    {#if pg.Message}<p class="err" role="alert">{pg.Message}</p>{/if}
    {#if pg.SSO}
      <a class="button primary block" href="/login/sso">Sign in with single sign-on</a>
      <div class="divider"><span>or</span></div>
    {/if}
    <form method="post" action="/login" class="stack" onsubmit={submit}>
      <label for="email">Email</label>
      <input id="email" name="email" type="email" required autocomplete="username" />
      <label for="password">Password</label>
      <input id="password" name="password" type="password" required autocomplete="current-password" />
      {#if pg.Next}<input type="hidden" name="next" value={pg.Next} />{/if}
      <button type="submit" class="primary" disabled={busy}>Sign in</button>
    </form>
    <p class="muted small">
      Local accounts keep working whether or not single sign-on is configured, so an identity provider being down is not a locked door.
    </p>
  </section>
</main>
