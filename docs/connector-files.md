# Files connector

Browse OneDrive, SharePoint and Google Drive from Hearth, then link a file to a Hearth document or import a copy of it. Available for Google Workspace and Microsoft 365. Off until an administrator turns it on.

## Browse

Open Cloud files from Settings, or choose Add from OneDrive, SharePoint or Drive on a document. Hearth lists a person's own drive and the places they can reach: the document libraries of SharePoint sites they follow, and Google shared drives. What they see is exactly what Microsoft or Google says they may see, listed live, with nothing cached in Hearth.

## Link or import

| Choice | What happens |
|---|---|
| Link | Hearth records where the file lives. The document shows a link back to the original |
| Import a copy | Hearth downloads the file once and attaches the copy to the document, and keeps the link as well |

Both need permission to edit the document, which Hearth checks. A Google Docs, Sheets or Slides file is imported as Word, Excel or PowerPoint. Imports are limited to 50 MB, and a larger file is linked instead. Removing a link does not remove an imported copy.

## Permissions

| Provider | Permission |
|---|---|
| Google Workspace | `drive.readonly` |
| Microsoft 365 | `Files.Read.All` and `Sites.Read.All` (delegated). SharePoint needs the second, and a tenant may need an administrator to consent to it |

Hearth never writes to a drive. Without `Sites.Read.All`, OneDrive still works and SharePoint libraries do not appear.
