# editor/

Source for `internal/web/static/editor.js`: the rich-text editor in Hearth's writing boxes (a
document's Details, comments, comment edits, the quick view's comment box). Bold shows as bold
while you type; what gets saved is still Markdown, exactly the Markdown
`internal/platform/markdown` renders.

**Nothing here runs at build or deploy time.** `editor.js` is committed, like `htmx.min.js`, so
`go build` still produces the whole application with no Node. You only need Node to change the
editor.

```
cd editor
npm ci          # exact versions from package-lock.json
npm test        # Markdown round-trips through the real configuration
npm run build   # writes internal/web/static/editor.js and editor-licenses.txt
```

Commit the rebuilt `editor.js` and `editor-licenses.txt` together with the source change.

## How it fits

- A `<textarea data-rich>` stays in the form and stays what is submitted. `src/editor.js` hides it,
  puts the editor beside it, and writes Markdown back into it on every change. With scripting off
  the textarea is simply there.
- The toolbar is `ui.js` (`data-md`). With an editor attached it calls `textarea.hearthRich`;
  without one it edits the raw text as before. **Markdown** in the toolbar switches a box between
  the two.
- `src/hearth-md.js` decides what the editor may produce and how it writes it down:
  - only what the renderer supports (no strikethrough or underline);
  - links only `http(s)://` or `/...`;
  - a bare link stays bare, so a YouTube address alone on a line still plays, and a Hearth link
    still becomes a card;
  - no HTML entities (the renderer escapes everything itself);
  - backslash escapes only for ``` ` * _ [ ] # > + - . ) ```, the same list as `escapable` in
    `markdown.go`. A backslash before anything else is just a backslash, so `\\server\share` and
    `C:\Users` survive a round trip. Change one list, change the other.

## Licences

Tiptap, ProseMirror, marked and their dependencies, all MIT. `build.mjs` refuses to build if
anything bundled is not MIT, and writes every bundled package's licence into
`internal/web/static/editor-licenses.txt`. Tiptap's paid Pro extensions and Cloud are not used.
