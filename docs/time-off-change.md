# Changing and cancelling time off

An approver or an admin can change or cancel anybody's time off request, pending or already approved, from the Time off page, over the API, and as assistant tools. The person is told and every change has an audit row.

## Who

The same people who may decide the person's requests: their named approver, a manager of their department, a super admin, and anybody holding People. Never the person themselves through this route. The person keeps their own Cancel for a pending request, and for an approved one that has not started.

A denied or cancelled request is finished and nobody changes it.

## What

Dates, type of leave, half a day (the first day counts as half), the reason and the note on the request. A change that changes nothing is refused, and the result has to be a real request (dates in order, a working day in them, a known type), the same checks as asking.

* A pending request is updated where it is.
* An approved request is updated where it is, decided again by whoever changed it, and its "Out" on the calendar moves with it.
* Cancelling an approved request takes its "Out" off the calendar. The request stays on the person's list as cancelled, with who cancelled it and why.

## The page

On Time off, under "To decide", each pending request and each approved one still to come or under way has "Change or cancel". Approved requests stay on that list until they end, however long ago they were approved, so they can be found. A cancelled request shows no actions and says who cancelled it and why. Both people see half days as taken: five working days with a half day is 4.5.

## The API and the tools

| Route | Tool | |
|---|---|---|
| `GET /v1/time-off?scope=decide\|mine` | `time_off_list` | The requests the caller may change (default), or their own. |
| `GET /v1/time-off/{id}` | `time_off_get` | One request, for its person or anybody who may decide it. |
| `PATCH /v1/time-off/{id}` | `time_off_change` | `starts_on`, `ends_on` (YYYY-MM-DD), `kind`, `half_day`, `reason`, `comments`, and `note`, which is why it was changed. Only the fields sent change. |
| `POST /v1/time-off/{id}/cancel` | `time_off_cancel` | `note` is optional. |

A write needs a write token. The tools are the routes run through their own handlers with the caller's own identity, so they answer exactly what the routes answer.

## The audit log

`time_off.change` and `time_off.cancel`, one row each, with the person, status, dates, days, half day and type before and after, and the note. The reason and comments text is never copied into the log (a leave reason is often medical); the row says only that they changed. In Postgres the row is written in the change's own transaction.

## EmberOps

With `HEARTH_BACKEND=emberops` the request is a `Hearth Time Off Request`, or an HRMS Leave Application on a site with HRMS (the addon hides the difference), and the same actions work on it.

* Pending: an ordinary update of the document.
* Approved on `Hearth Time Off Request`: an update of the document, decided again by whoever changed it, and the calendar Event is replaced.
* Approved on HRMS: a submitted Leave Application can only be cancelled and annotated, so a change is what HRMS asks for. The old application is cancelled and its Event removed, and a new approved one is filed with the new dates under a new Hearth id. The person's list shows the old one cancelled ("Changed by ... and filed again") and the new one approved. When HRMS refuses the new one (a leave balance, an overlap) the original is filed again as it was, so a failed change costs nobody their time off.
* Cancelling: EmberOps cancels the document rather than deleting it, so it stays, cancelled.

The audit row is written in Hearth's database in both modes.

## Vacation that approves itself

A person whose time is approved by standing order (the auto-approve setting a super admin turns on for salaried staff) takes vacation without waiting on anybody, up to five working days in a calendar month. The request is approved when it is filed, by the setting and not by a person: the Out goes on the calendar, the requester is told, and a `time_off.auto_approve` audit row is written that names no actor. The days already approved in that month, in either back end, count toward the five, and a request that spans two months is counted in each. A request that would pass five working days is filed pending for the usual decision, and a person can still submit anything. Other kinds of leave always wait for a person.
