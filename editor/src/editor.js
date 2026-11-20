// The rich-text editor for Hearth's writing boxes: bold shows as bold while
// you type. Built into internal/web/static/editor.js (see README.md).
//
// Progressive enhancement, never a replacement: a <textarea data-rich> stays
// in the form and stays what gets submitted. The editor sits beside it, hides
// it, and writes Markdown back into it on every change, so the server, the
// Preview button and a browser with scripting off all see a plain textarea.
//
// The toolbar (ui.js, data-md) talks to the editor through textarea.hearthRich
// when one is attached, and edits the raw text when it is not.

import { Editor } from '@tiptap/core'
import { Markdown } from '@tiptap/markdown'
import { Placeholder } from '@tiptap/extensions'
import { Marked } from 'marked'
import { extensions, patchManager, hearthMarked } from './hearth-md.js'

patchManager()

// The non-breaking space, U+00A0, spelled so no tool can quietly swap it.
const NBSP = new RegExp(String.fromCharCode(160), 'g')

function attach(box) {
  if (box.hearthRich || box.closest('[data-rich-off]')) return
  const holder = document.createElement('div')
  holder.className = 'richbox'
  holder.style.setProperty('--rows', box.getAttribute('rows') || '4')
  box.after(holder)

  const editor = new Editor({
    element: holder,
    injectCSS: false,
    extensions: [
      ...extensions(),
      Markdown.configure({ marked: hearthMarked(new Marked()) }),
      Placeholder.configure({ placeholder: box.getAttribute('placeholder') || '' }),
    ],
    content: box.value,
    contentType: 'markdown',
    editorProps: {
      attributes: {
        class: 'prose richtext',
        role: 'textbox',
        'aria-multiline': 'true',
        'aria-label': box.getAttribute('aria-label') || box.getAttribute('placeholder') || 'Text',
      },
    },
    onUpdate: () => sync(),
  })

  // The form submits the textarea, so it must always hold the current text.
  // An input event, so the Preview and anything else listening still hears it.
  function sync() {
    // Trailing blank lines are the editor's empty last paragraph, not text.
    // Non-breaking spaces are how the editor keeps a space beside bold or a
    // link; saved, they are just spaces, or lines would refuse to wrap there.
    const md = editor.isEmpty ? '' : editor.getMarkdown().replace(NBSP, ' ').replace(/\s+$/, '')
    if (box.value === md) return
    box.value = md
    box.dispatchEvent(new Event('input', { bubbles: true }))
  }

  // A hidden required field cannot show its "fill this in" bubble, and the
  // browser then refuses to submit without saying why. So the check moves to
  // the editor: an empty one stops the submit and takes the cursor.
  const required = box.required
  box.required = false
  box.hidden = true
  const form = box.form
  if (form) form.classList.add('rich-on')
  if (form && required) {
    form.addEventListener('submit', (ev) => {
      sync()
      if (!box.hidden || box.value.trim() !== '') return
      ev.preventDefault()
      ev.stopImmediatePropagation()
      editor.commands.focus()
      holder.classList.add('richbox-empty')
      setTimeout(() => holder.classList.remove('richbox-empty'), 1500)
    }, true)
  }

  box.hearthRich = {
    editor,
    format(kind) {
      const c = editor.chain().focus()
      if (kind === 'bold') c.toggleBold().run()
      else if (kind === 'italic') c.toggleItalic().run()
      else if (kind === 'list') c.toggleBulletList().run()
      else if (kind === 'checklist') c.toggleTaskList().run()
      else if (kind === 'heading') c.toggleHeading({ level: 2 }).run()
      else if (kind === 'quote') c.toggleBlockquote().run()
      else if (kind === 'code') c.toggleCodeBlock().run()
    },
    link(href) {
      const { empty } = editor.state.selection
      if (empty) {
        editor.chain().focus().insertContent({ type: 'text', text: href, marks: [{ type: 'link', attrs: { href } }] }).run()
      } else {
        editor.chain().focus().extendMarkRange('link').setLink({ href }).run()
      }
    },
    // A link alone on a line of its own -- what makes a YouTube address play.
    block(href) {
      editor.chain().focus().insertContent([
        { type: 'paragraph', content: [{ type: 'text', text: href, marks: [{ type: 'link', attrs: { href } }] }] },
        { type: 'paragraph' },
      ]).run()
    },
    // Show the Markdown itself instead, for whoever wants it -- and as the
    // way out should the editor ever get something wrong.
    toggleRaw() {
      if (box.hidden) {
        sync()
        box.hidden = false
        holder.hidden = true
        box.focus()
        return true
      }
      editor.commands.setContent(box.value, { contentType: 'markdown' })
      box.hidden = true
      holder.hidden = false
      editor.commands.focus('end')
      return false
    },
    selectedText() {
      const { from, to } = editor.state.selection
      return editor.state.doc.textBetween(from, to, ' ')
    },
  }

  // A form that is reset (after posting a comment, say) empties the
  // textarea; the editor follows it.
  if (form) form.addEventListener('reset', () => setTimeout(() => editor.commands.setContent(box.value, { contentType: 'markdown' })))
}

function scan(root) {
  if (root.matches && root.matches('textarea[data-rich]')) attach(root)
  root.querySelectorAll && root.querySelectorAll('textarea[data-rich]').forEach(attach)
}

scan(document)
// The quick view and pages the app draws after this script loaded arrive later, and announce themselves (hearth:load).
document.addEventListener('hearth:load', (ev) => scan(ev.target))
