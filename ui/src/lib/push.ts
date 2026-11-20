// Web Push in the browser: the small parts of turning it on that do not need a page, so a test can run them.

/** A VAPID public key as the browser wants it: the URL-safe base64 string from the server, as bytes. */
export function keyBytes(b64: string): Uint8Array<ArrayBuffer> {
  const pad = '='.repeat((4 - (b64.length % 4)) % 4);
  const raw = atob((b64 + pad).replace(/-/g, '+').replace(/_/g, '/'));
  const out = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

/** What to call this device in the person's list, from the browser's own description of itself. */
export function deviceLabel(ua: string): string {
  const os = /iPhone/.test(ua) ? 'iPhone' : /iPad/.test(ua) ? 'iPad' : /Android/.test(ua) ? 'Android' : /Windows/.test(ua) ? 'Windows' : /Mac OS X|Macintosh/.test(ua) ? 'Mac' : /Linux|X11/.test(ua) ? 'Linux' : 'Device';
  const browser = /Edg\//.test(ua) ? 'Edge' : /OPR\//.test(ua) ? 'Opera' : /Firefox\//.test(ua) ? 'Firefox' : /Chrome\/|CriOS\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : '';
  return browser ? `${os} ${browser}` : os;
}

/** What stops this browser from ringing, in words, or '' when it can. iPhones only ring an app added to the Home Screen. */
export function whyNot(env: { sw: boolean; push: boolean; notification: boolean; ua: string; standalone: boolean }): string {
  if (env.sw && env.push && env.notification) return '';
  if (/iPhone|iPad/.test(env.ua) && !env.standalone) {
    return 'On an iPhone or iPad, add Hearth to your Home Screen first (Share, then Add to Home Screen), open it from there, and turn notifications on in the app.';
  }
  return 'This browser cannot receive push notifications. Try Chrome, Edge, Firefox or Safari, or install Hearth from the browser menu.';
}
