# Files connector

OneDrive, SharePoint and Google Drive inside Hearth, read and write, as the person. Available for Google Workspace and Microsoft 365. Off until an administrator turns it on.

## Open and change files

Cloud files in Settings lists a person's own drive and the places they can reach: the document libraries of SharePoint sites they follow, and Google shared drives. What they see is exactly what Microsoft or Google says they may see, listed live, with nothing cached in Hearth. From there they can:

- upload a file into the folder being shown
- replace a file's content
- rename a file or folder, move it into another folder, and make new folders
- delete a file or folder, which sends it to the provider's bin where it can be restored

## Link, import and save out

| Choice | What happens |
|---|---|
| Link | Hearth records where the file lives. The document shows a link back to the original |
| Import a copy | Hearth downloads the file once and attaches the copy to the document, and keeps the link as well |
| Save to a drive | A file attached to a Hearth document is copied into the folder being shown |

Linking and importing need permission to edit the document, and saving an attachment out needs permission to see it, which Hearth checks. A Google Docs, Sheets or Slides file is imported as Word, Excel or PowerPoint. Imports and uploads are limited to 50 MB. Removing a link does not remove an imported copy.

## Permissions

| Provider | Permission |
|---|---|
| Google Workspace | `https://www.googleapis.com/auth/drive` |
| Microsoft 365 | `Files.ReadWrite.All` and `Sites.Read.All` (delegated). The second finds the SharePoint sites a person follows, and a tenant may need an administrator to consent to it |

Without `Sites.Read.All`, OneDrive still works and SharePoint libraries do not appear.
