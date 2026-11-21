# Giving one folder to one person

A manager can give a person view, comment or edit access to one folder, at any depth. The person reaches that folder and everything under it, and nothing above it or beside it. This is how a contractor, or anyone, is handed a single subfolder of a department.

## How a manager does it

1. Open **Spaces**, find the folder, and choose **Access** beside it.
2. Type the email address the person signs in with, choose **View**, **View and comment** or **View, comment and edit**, and optionally a last day.
3. Choose **Give access**.

The same page lists who holds access through this folder, with their level and end date, and **Take back** removes it. Taking it back is immediate: the person's next request no longer finds the folder, the work in it, or a search hit for it.

Who may do this: a super admin, a manager of the department, or a manager of any folder above this one. Somebody who was given edit on a folder cannot hand it on.

## What the person sees

Everything in the folder and below it: its folders, its documents, its tasks (the list, each task page, comments, subtasks, attachments, the board and the views of that folder), and search hits for them. What they may do follows the level they were given: view reads, comment adds comments, edit changes and adds tasks and documents in the folder.

Nothing else. The folders above are not shown, not named in the path at the top of the page, and not counted. Siblings and their contents answer "not found", the same answer a link to something that does not exist gets. Notifications about work they can no longer open are hidden from their inbox and from the unread count.

Work made in the folder after the access was given reaches the person on its own, whoever makes it and however it is made (a page, the API, a template, an import). Work moved out of the folder stops reaching them, and work moved in starts to. A folder moved out of the folder takes its access with it.

## Last days

A last day ends access at the end of that day, Eastern time, on every path at once: the tree, pages, tasks, search and the inbox. The row stays on the page marked **ended** so the manager can see what lapsed, and giving access again replaces it.

## Contractor accounts

A person who is not part of the company can hold a folder like anyone else. An owner or super admin can also mark the account a **contractor account** from the person's page. A contractor account is in no department and does not reach the open Daily reports department that every other account does, so what it sees is only what it was given.

## Managers and super admins

Whoever may give a folder (a manager of it, or of any folder above it) can also open it and everything under it, at view level at least, without being a member of the department or holding a grant. It only adds: a level somebody already holds is never lowered. It ends when they stop managing the folder, unless they manage something above it too. This opening is not a grant given from the People page and is not shown there or on a folder's Access page.

A super admin is never narrowed by any of this. They open every folder at every depth, see every parent's full name, see everybody in every list, and can give and take back a folder anywhere, even on an account marked as a contractor's.

## What a contractor account can reach

A contractor account is let into the folder and the work in it (the Spaces and Projects pages, documents, tasks, work orders, views), search, its own inbox and its own account page, and nothing else. Every other address (contacts, companies, deals, channels, the calendar, daily reports, time, people, teams, settings, plant and the rest) answers "no such page" whatever the page would have shown, and so does a queued offline write to one. A page added later is closed to contractor accounts until it is put on the list. The rail shows only what the account can use, its front page is Spaces, and it is never asked which department it is in. A super admin is never narrowed, even on an account marked as a contractor's.

## Lists of people for a contractor account

An assignee picker, a filter by person, a mention: for a contractor account each is cut to the person themselves and the people who hold a grant of their own on a folder the account can open (the managers of that folder, whoever made it, and anyone else it was given to). Everybody else gets those lists as they always did. A name is still shown on work the contractor can open, because it is part of that work.

## What this does not change

- Nothing anybody holds today is taken away. Access given from the People page (a department) is a different grant and is never changed or removed by this one. If a person already holds a folder that way, giving it again here leaves their existing level alone.
- A person who reaches a folder through a delegation made higher up cannot be given the same folder again; change it where it was given.
- Only access given from this page is listed on it. People who reach the folder because they manage or belong to a department above it are not.

## For operators

- Migration `00063` adds `grants.via_space_id` (the folder a row was delegated from), `principals.contractor`, and two triggers, on `documents` and `spaces`, that keep the delegated rows true when something is created or moved. A row with no `via_space_id` is any other grant and is never touched by a delegation.
- Every give and every take back writes a `grant.add` or `grant.remove` row to the audit log against the folder, naming who, whom, the level, the end date and how many folders and documents it covered. Marking a contractor account writes `person.contractor`.
- The search cluster answers from the subject list it was given at index time, so its answers are checked against the live grants before they are shown: a delegation that ended or was taken back stops matching at once, before the index has heard.
- With `HEARTH_BACKEND=emberops`, Hearth still decides access. Every folder under a delegation carries its own grant row, which the space copy sends to EmberOps with its end date, and EmberOps's rules read a grant on a folder or any folder above it, so a grant on a subfolder reaches that subfolder and what is under it and does not reach its parent.
