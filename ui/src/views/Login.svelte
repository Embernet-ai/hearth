<script lang="ts">
  import type { Envelope } from '../lib/types';
  import { post } from '../lib/api';

  let { env, onEnv }: { env: Envelope; onEnv: (e: Envelope, url: string) => void } = $props();
  const pg = $derived(env.page);
  let busy = $state(false);
  // The provider's name on the button; "single sign-on" when the install did not name it. The form is off only when the
  // install says so and there is a provider button to take its place.
  const ssoName = $derived(pg.SSOName || 'single sign-on');
  const isVord = $derived(ssoName.toLowerCase() === 'vord');
  const showForm = $derived(!(pg.SSO && pg.HideLocalLogin));

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

<!-- The EmberOps door: the logo, "Login to ...", one white card, the form, "or", and the provider button. An install
     whose only way in is its provider turns the form off (HEARTH_LOCAL_LOGIN_FORM=false) and the button is the whole door. -->
<div class="door" role="main">
  <header class="door-head">
    <img class="door-logo" src="/favicon.svg" alt="Hearth" />
    <h1 class="door-title">Login to Hearth</h1>
  </header>
  <section class="door-card">
    {#if pg.Message}<p class="err door-err" role="alert">{pg.Message}</p>{/if}
    {#if !showForm}
      <a class="door-btn" href="/login/sso">
        {#if isVord}<img class="door-btn-icon" src="/vord.svg" alt="" />{/if}
        Login with {ssoName}
      </a>
    {:else}
      <form method="post" action="/login" class="door-form" onsubmit={submit}>
        <label class="sr-only" for="email">Email</label>
        <input id="email" name="email" type="email" placeholder="jane@example.com" required autocomplete="username" />
        <label class="sr-only" for="password">Password</label>
        <input id="password" name="password" type="password" placeholder="Password" required autocomplete="current-password" />
        {#if pg.Next}<input type="hidden" name="next" value={pg.Next} />{/if}
        <button type="submit" class="door-primary" disabled={busy}>Login</button>
      </form>
      {#if pg.SSO}
        <p class="door-or">or</p>
        <a class="door-btn" href="/login/sso">
          {#if isVord}<img class="door-btn-icon" src="/vord.svg" alt="" />{/if}
          Login with {ssoName}
        </a>
      {/if}
      <p class="door-fine">
        Local accounts keep working whether or not single sign-on is configured, so an identity provider being down is not a locked door.
      </p>
    {/if}
  </section>
  <p class="door-link"><a href="/connect">Use a different Hearth server</a></p>
</div>
