<script lang="ts">
  // The file chooser on the import pages. The real <input type="file"> stays in the form, so the page posts exactly what it always
  // did; this draws the button and the drop area around it, in the app's own buttons, with the chosen name and a plain error.
  // Drag and drop puts the dropped file into that same input, and a file the page would refuse (wrong kind, or empty) is said so
  // before anything is sent.
  let { id, name = 'file', accept = '', required = false, kind = 'file' }: { id: string; name?: string; accept?: string; required?: boolean; kind?: string } = $props();

  let input = $state<HTMLInputElement | null>(null);
  let chosen = $state('');
  let size = $state(0);
  let problem = $state('');
  let over = $state(false);

  const exts = $derived(accept.split(',').map((a) => a.trim().toLowerCase()).filter((a) => a.startsWith('.')));
  const words = $derived(exts.length ? exts.join(' or ') : '');

  function judge(f: File | undefined): string {
    if (!f) return '';
    if (f.size === 0) return `${f.name} is empty. Choose a ${kind} with something in it.`;
    const lower = f.name.toLowerCase();
    if (exts.length && !exts.some((e) => lower.endsWith(e))) return `${f.name} is not a ${kind}. Choose a file ending in ${words}.`;
    return '';
  }

  function take(f: File | undefined) {
    problem = judge(f);
    chosen = f && !problem ? f.name : '';
    size = f ? f.size : 0;
    // a refused file is not kept, so the form cannot send it
    if (problem && input) input.value = '';
  }
  function onchange() { take(input?.files?.[0]); }
  function ondrop(e: DragEvent) {
    e.preventDefault();
    over = false;
    const f = e.dataTransfer?.files?.[0];
    if (!f || !input) return;
    const t = new DataTransfer();
    t.items.add(f);
    input.files = t.files;
    take(f);
    if (problem) input.value = '';
  }
  function oninvalid() { if (!chosen) problem = `Choose a ${kind} first, or drop one here.`; }
  const bytes = (n: number) => (n < 1024 ? `${n} B` : n < 1048576 ? `${(n / 1024).toFixed(1)} KB` : `${(n / 1048576).toFixed(1)} MB`);
</script>

<div class="filepick" class:over class:has={!!chosen && !problem} class:bad={!!problem} data-filepick
  ondragover={(e) => { e.preventDefault(); over = true; }} ondragleave={() => (over = false)} {ondrop} role="group" aria-label="Choose a file">
  <input bind:this={input} {id} {name} type="file" {accept} {required} class="filepick-input" onchange={onchange} oninvalid={oninvalid}
    aria-describedby={id + '-note'}>
  <label class="button filepick-button" for={id}>{chosen ? 'Choose a different file' : 'Choose file'}</label>
  <span class="filepick-note" id={id + '-note'} aria-live="polite">
    {#if problem}<span class="err" data-filepick-error>{problem}</span>
    {:else if chosen}<strong class="filepick-name" data-filepick-name>{chosen}</strong> <span class="muted">{bytes(size)}</span>
    {:else}<span class="muted">or drop a {kind} here{#if words} ({words}){/if}</span>{/if}
  </span>
</div>
