export {}; // a module, so the top level may await
// The interface drawn on the server side, for the acceptance suite (cmd/acceptance). It reads one page's data (the JSON
// the server sends to the interface) on standard input and writes the markup the browser would draw for it: the same
// views, the same shell. The suite then checks what a person is shown with the checks it always had, against the real
// views, instead of against a stand-in.

// The interface touches the address bar when it starts; there is none here.
const g = globalThis as any;
g.location = { pathname: '/', search: '', origin: 'http://hearth.invalid', href: 'http://hearth.invalid/' };
g.addEventListener = () => {};
g.sessionStorage = { getItem: () => null, setItem: () => {} };

const { render } = await import('svelte/server');
const { default: Shell } = await import('./components/Shell.svelte');
const { default: Login } = await import('./views/Login.svelte');
const { default: Anonymous } = await import('./components/Anonymous.svelte');

const chunks: Buffer[] = [];
for await (const c of process.stdin) chunks.push(c as Buffer);
const env = JSON.parse(Buffer.concat(chunks).toString('utf8'));

// The same three cases as the app (components/App.svelte).
let body: string;
if (env.template === 'login') {
  body = render(Login as any, { props: { env, onEnv: () => {} } }).body;
} else if (!env.page.Identity) {
  body = '<main class="centered-page">' + render(Anonymous as any, { props: { env, setEnv: () => {} } }).body + '</main>';
} else {
  body = render(Shell as any, { props: { env, epoch: 0, onEnv: () => {}, onSignOut: () => {} } }).body;
}
process.stdout.write(body);
