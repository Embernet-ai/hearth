// Round-trips Markdown through the editor's real configuration (src/hearth-md.js)
// and fails if anything Hearth writes would not come back the same.
// Run: npm test
import { Marked } from 'marked'
import { extensions, patchManager, hearthMarked, MarkdownManager } from '../src/hearth-md.js'

patchManager()
const m = new MarkdownManager({ extensions: extensions(), marked: hearthMarked(new Marked()) })

// Written as String.raw so a backslash in a sample is one backslash.
const r = String.raw
const same = [
  r`Hello **bold** and *italic* and [a link](https://example.com)`,
  'line one\nline two',
  '- one\n- two\n\n1. first\n2. second',
  '- [ ] todo\n- [x] done',
  '# Title\n\n> quoted\n\n```\ncode *here* ' + r`\\x` + '\n```\n\nuse `x*y` inline',
  r`file\_name and 5\*3 and a # sign and a < b & c <b>x</b>`,
  'Intro:\nhttps://youtu.be/dQw4w9WgXcQ\nEnd',
  'https://youtu.be/ab_cd-EF_12',
  'see https://example.com/a_b_c here',
  'https://hearth.local/documents/8d186793-1345-47ab-81a5-73f739c7d37e',
  r`C:\Users\x and \\server\share`,
  r`\# not a heading`,
  r`\- not a list`,
  r`1\. not a list`,
  r`\> not a quote`,
]
let bad = 0
for (const s of same) {
  const out = m.serialize(m.parse(s))
  if (out !== s) {
    bad++
    console.log('CHANGED', JSON.stringify(s), '\n     =>', JSON.stringify(out))
  }
}

// Text as typed into the editor (one text node) -> what gets saved.
const typed = [
  ['a < b & c <b>x</b>', 'a < b & c <b>x</b>'],
  ['5*3 file_name [x]', r`5\*3 file\_name \[x\]`],
  ['# not a heading', r`\# not a heading`],
  ['> not a quote', r`\> not a quote`],
  ['1. not a list', r`1\. not a list`],
  [r`C:\Users and \\server`, r`C:\Users and \\server`],
]
for (const [t, want] of typed) {
  const out = m.serialize({ type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text: t }] }] })
  if (out !== want) {
    bad++
    console.log('TYPED', JSON.stringify(t), '=>', JSON.stringify(out), 'want', JSON.stringify(want))
  }
}
console.log(bad ? `${bad} problem(s)` : 'all round-trips ok')
process.exit(bad ? 1 : 0)
