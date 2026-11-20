<script lang="ts">
  // Turn push notifications on for this device, choose which ones ring, and try it. The server's address
  // for each action is in internal/web/pushpage.go; everything about the browser is in lib/push.ts.
  import { onMount } from 'svelte';
  import { keyBytes, deviceLabel, whyNot } from '../lib/push';

  type Kind = { id: string; label: string; help: string };
  type Device = { id: string; label: string; endpoint: string; created_at: string; last_ok_at?: string; last_status: number };
  type Info = { enabled: boolean; key?: string; devices?: Device[]; kinds?: Kind[]; on?: string[] };

  let info = $state<Info | null>(null);
  let problem = $state('');
  let notice = $state('');
  let busy = $state(false);
  let blocked = $state('');
  let thisEndpoint = $state('');
  let denied = $state(false);

  const here = $derived((info?.devices ?? []).find((d) => d.endpoint === thisEndpoint));
  const others = $derived((info?.devices ?? []).filter((d) => d.endpoint !== thisEndpoint));

  async function call(path: string, body?: unknown) {
    const res = await fetch(path, {
      method: body === undefined ? 'GET' : 'POST',
      headers: body === undefined ? { Accept: 'application/json' } : { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'same-origin',
    });
    const ct = res.headers.get('Content-Type') ?? '';
    if (!ct.includes('application/json')) throw new Error('Sign in again to change this.');
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || `The server answered ${res.status}.`);
    return data;
  }

  async function load() {
    try {
      info = (await call('/push/state')) as Info;
      const reg = await navigator.serviceWorker?.ready;
      const sub = await reg?.pushManager?.getSubscription();
      thisEndpoint = sub?.endpoint ?? '';
    } catch (e: any) {
      problem = e.message;
    }
  }

  onMount(() => {
    blocked = whyNot({
      sw: 'serviceWorker' in navigator,
      push: 'PushManager' in window,
      notification: 'Notification' in window,
      ua: navigator.userAgent,
      standalone: (navigator as any).standalone === true || matchMedia('(display-mode: standalone)').matches,
    });
    denied = 'Notification' in window && Notification.permission === 'denied';
    load();
  });

  async function turnOn() {
    problem = notice = '';
    busy = true;
    try {
      // The permission prompt has to come from this click, before any waiting.
      const perm = await Notification.requestPermission();
      if (perm !== 'granted') {
        denied = perm === 'denied';
        problem = 'Notifications are blocked for Hearth in this browser. Allow them in the browser or phone settings, then try again.';
        return;
      }
      const reg = await navigator.serviceWorker.ready;
      let sub = await reg.pushManager.getSubscription();
      if (sub) {
        // One made against a different key (the installation's key changed) can never be sent to.
        const have = sub.options?.applicationServerKey ? new Uint8Array(sub.options.applicationServerKey) : null;
        const want = keyBytes(info!.key!);
        if (have && (have.length !== want.length || have.some((b, i) => b !== want[i]))) {
          await sub.unsubscribe();
          sub = null;
        }
      }
      sub ??= await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(info!.key!) });
      await call('/push/subscribe', { ...sub.toJSON(), label: deviceLabel(navigator.userAgent) });
      await load();
      notice = 'Notifications are on for this device.';
    } catch (e: any) {
      problem = e.message;
    } finally {
      busy = false;
    }
  }

  async function turnOff() {
    problem = notice = '';
    busy = true;
    try {
      const reg = await navigator.serviceWorker.ready;
      const sub = await reg.pushManager.getSubscription();
      if (sub) {
        await call('/push/unsubscribe', { endpoint: sub.endpoint });
        await sub.unsubscribe();
      }
      await load();
      notice = 'Notifications are off for this device.';
    } catch (e: any) {
      problem = e.message;
    } finally {
      busy = false;
    }
  }

  async function test(endpoint: string) {
    problem = notice = '';
    busy = true;
    try {
      const r = await call('/push/test', { endpoint });
      if (r.sent) notice = 'Sent. It should arrive in a moment.';
      else if (r.removed) { problem = 'That device is no longer reachable and was removed. Turn notifications on again.'; await load(); }
      else problem = 'The push service did not accept it. Try turning notifications off and on again.';
    } catch (e: any) {
      problem = e.message;
    } finally {
      busy = false;
    }
  }

  async function removeDevice(d: Device) {
    problem = notice = '';
    try {
      await call('/push/unsubscribe', { id: d.id });
      await load();
    } catch (e: any) {
      problem = e.message;
    }
  }

  async function toggleKind(id: string, on: boolean) {
    problem = notice = '';
    const next = new Set(info?.on ?? []);
    if (on) next.add(id); else next.delete(id);
    try {
      await call('/push/kinds', { kinds: [...next] });
      info = { ...info!, on: [...next] };
    } catch (e: any) {
      problem = e.message;
      await load();
    }
  }
</script>

<section data-push>
  <h3 id="push">Notifications on this device</h3>
  {#if info && !info.enabled}
    <p class="muted small">Push notifications are not set up on this Hearth.</p>
  {:else if blocked}
    <p class="muted small" data-push-blocked>{blocked}</p>
  {:else if !info}
    <p class="muted small">Checking...</p>
  {:else}
    <p class="muted small">
      Hearth can ring this device when something needs you, even with the app closed. A notification shows the title of the item only. Tapping it opens the item after you sign in.
    </p>
    {#if problem}<p class="err" data-push-problem>{problem}</p>{/if}
    {#if notice}<p class="muted small" data-push-notice>{notice}</p>{/if}
    <div class="row">
      {#if here}
        <button type="button" class="primary" data-push-test disabled={busy} onclick={() => test(here.endpoint)}>Send a test notification</button>
        <button type="button" data-push-off disabled={busy} onclick={turnOff}>Turn off on this device</button>
      {:else}
        <button type="button" class="primary" data-push-on disabled={busy || denied} onclick={turnOn}>Turn on notifications</button>
      {/if}
    </div>
    {#if denied && !here}<p class="muted small">This browser has blocked notifications for Hearth. Allow them in its site settings, then come back.</p>{/if}

    <h4>What rings</h4>
    <ul class="plain" data-push-kinds>
      {#each info.kinds ?? [] as k (k.id)}
        <li>
          <label>
            <input type="checkbox" checked={(info.on ?? []).includes(k.id)} onchange={(e) => toggleKind(k.id, e.currentTarget.checked)}>
            {k.label}
          </label>
          <span class="muted small">{k.help}</span>
        </li>
      {/each}
    </ul>
    <p class="muted small">Quiet hours above hold these too. The notification is still in your inbox.</p>

    {#if others.length}
      <h4>Your other devices</h4>
      <ul class="plain" data-push-devices>
        {#each others as d (d.id)}
          <li>
            {d.label || 'A device'}
            <button type="button" class="ghost small" disabled={busy} onclick={() => test(d.endpoint)}>Test</button>
            <button type="button" class="ghost small" onclick={() => removeDevice(d)}>Remove</button>
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</section>
