# Calendar connector

Keeps a person's Hearth calendar and their Google Calendar or Outlook calendar the same, in both directions. Available for Google Workspace and Microsoft 365. Off until an administrator turns it on.

## What syncs

A window of a week back to ninety days ahead, with recurring events expanded to their instances. Each event keeps its title, notes, place and time, and all-day events keep their days.

| Where it changed | What happens |
|---|---|
| A new event at Google or Microsoft | It appears in Hearth |
| A new event in Hearth, made by the person | It appears at Google or Microsoft |
| An event changed at Google or Microsoft | Hearth takes the change |
| An event changed in Hearth | Google or Microsoft takes the change |
| Changed in both since the last run | The provider's version wins, and the log says so |
| Deleted on one side, inside the window | Deleted on the other |

Work that has a due date, maintenance schedules and logged hours are not copied: Hearth reads them where they live, as it always did.

## Meet and Teams links

An administrator can turn on a link for events Hearth creates. Google Workspace gets a Google Meet link and Microsoft 365 gets a Teams meeting. It is added once, when the event is created in Hearth, and is not added to all-day events. A link already on an event made elsewhere is shown as its place when the event has none.

## Shared calendars and departments

Under Settings, Connectors a person sees every calendar they can read. Choosing one adds it to their own Hearth calendar. A manager of a department can instead send it to the department: its events become the department's, so everybody Hearth gives that department can see them, with no Google or Microsoft account of their own. Events added to the department in Hearth go back to that calendar. One calendar goes into one department, so two people who both see it cannot import it twice.

## Permissions

| Provider | Permission |
|---|---|
| Google Workspace | `calendar.events` and `calendar.calendarlist.readonly` |
| Microsoft 365 | `Calendars.ReadWrite` (delegated) |

## Limits

Events are matched in UTC, and all-day events by their date in UTC, so run Hearth with its clock in UTC. A person who disconnects keeps the events that came across, which are now theirs in Hearth, and loses the link to the provider.
