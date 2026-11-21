# Recorded Graph responses

These are Microsoft Graph v1.0 response bodies written in the shapes Microsoft publishes on Microsoft Learn, one file
per call, with every value invented. They are NOT captured from a live tenant (Hearth has none to capture from). They
exist so the adapter tests parse what Graph is documented to return: seven-digit fractional seconds on times, an
`@odata.nextLink` on a paged list, `changeKey` on events, `isCancelled`, an `onlineMeeting.joinUrl`, a `folder` or
`file` facet on a drive item.

Sources: learn.microsoft.com/graph/api/user-list-calendarview, user-post-events, user-list-contacts,
user-list-messages, drive-get, driveitem-list-children, driveitem-get-content, user-list-followedsites,
site-list-drives.
