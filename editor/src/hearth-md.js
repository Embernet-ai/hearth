// What the editor is allowed to make, and how it writes it down, so that what
// it saves is exactly the Markdown internal/platform/markdown renders -- no
// more, no less. Shared by the browser bundle (editor.js) and the round-trip
// test (test/roundtrip.mjs), so the test checks the real configuration.

import StarterKit from '@tiptap/starter-kit'
import { TaskList, TaskItem } from '@tiptap/extension-list'
import Link from '@tiptap/extension-link'
import { MarkdownManager } from '@tiptap/markdown'

// Hearth's renderer has no strikethrough or underline, so the editor must not
// offer them: text formatted that way would save as markup that shows up as
// stray symbols. Links are limited to what the renderer allows (http, https,
// and paths inside Hearth).
export function extensions() {
  return [
    StarterKit.configure({ strike: false, underline: false, link: false, heading: { levels: [1, 2, 3] } }),
    HearthLink.configure({
      openOnClick: false,
      autolink: true,
      linkOnPaste: true,
      defaultProtocol: 'https',
      isAllowedUri: (url) => /^(https?:\/\/|\/(?!\/))/i.test(url),
    }),
    TaskList,
    TaskItem.configure({ nested: false }),
  ]
}

const textOf = (node) => (node.content || []).map((c) => c.text || '').join('')

// A link whose text is its own address is written bare, not as
// [https://x](https://x). Hearth only turns a bare address alone on its line
// into a YouTube player or a card for something in Hearth, so the wrapped
// form would quietly stop both from working.
//
// Marks are rendered as an opening and a closing around a placeholder, with
// the real text in ctx.meta.markText; whole nodes come with their content.
const HearthLink = Link.extend({
  renderMarkdown(node, h, ctx) {
    const href = (node.attrs && node.attrs.href) || ''
    const text = ctx && ctx.meta && ctx.meta.markText != null ? ctx.meta.markText : textOf(node)
    if (text === href) return h.renderChildren(node)
    return `[${h.renderChildren(node)}](${href})`
  },
})

// The bare address itself is written exactly as it is, unescaped, so a
// YouTube id or a path with an underscore in it still matches on the server.
const isBareLink = (text, node) =>
  (node.marks || []).some((m) => m.type === 'link' && m.attrs && m.attrs.href === text)

// Tiptap's default text encoding is CommonMark's: HTML entities plus
// backslashes. Hearth's renderer does its own HTML escaping (everything,
// always), so entities would show up literally as "&lt;". Backslashes it
// understands, but only before these characters (markdown.go, ESCAPABLE):
// a backslash before anything else -- a Windows path, \\server\share -- is
// just a backslash, which is why a backslash itself is never escaped here.
export const ESCAPABLE = '`*_[]#>+-.)'
const INLINE = /([`*_[\]])/g

// The parser has to read backslashes the same way, or opening a note holding
// \\server\share would turn it into \server\share and save it like that.
// marked's own rule escapes any punctuation; this one only Hearth's set.
export const hearthMarked = (m) => {
  m.use({
    tokenizer: {
      escape(src) {
        const cap = /^\\([`*_[\]#>+\-.)])/.exec(src)
        if (cap) return { type: 'escape', raw: cap[0], text: cap[1] }
        if (src[0] === '\\') return { type: 'text', raw: '\\', text: '\\' }
        return false
      },
    },
  })
  return m
}
function encodeText(text, node, parent) {
  const inCode =
    (parent && parent.type && this.codeTypes.has(parent.type)) ||
    (node.marks || []).some((m) => this.codeTypes.has(typeof m === 'string' ? m : m.type))
  if (inCode || isBareLink(text, node)) return text
  let out = text.replace(INLINE, '\\$1')
  // A paragraph that merely starts with "# ", "- ", "> " or "1. " would come
  // back as a heading, list or quote; mark the first character literal.
  const first = parent && Array.isArray(parent.content) && parent.content[0] === node
  if (first && (!parent.type || parent.type === 'paragraph')) {
    out = out.replace(/^(\s*)([#>+-])(?=\s|$)/, '$1\\$2')
    out = out.replace(/^(\s*\d+)([.)])(?=\s)/, '$1\\$2')
    out = out.replace(/^(\s*)(---|\*\*\*|___)\s*$/, '$1\\$2')
  }
  return out
}

let patched = false
export function patchManager() {
  if (patched) return
  patched = true
  MarkdownManager.prototype.encodeTextForMarkdown = encodeText
}

export { MarkdownManager }
