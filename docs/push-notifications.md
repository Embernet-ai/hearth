# Push notifications

Hearth is an installable app (a PWA), and an installed Hearth can ring your phone or computer when something needs you, even with the app closed. This is Web Push, the standard every browser implements. There is no extra service to run.

## For people

1. Install Hearth.
   - iPhone and iPad (iOS 16.4 or newer): open Hearth in Safari, tap Share, then Add to Home Screen. Open Hearth from the new icon. iOS only offers notifications to an app opened from the Home Screen.
   - Android: open Hearth in Chrome and choose Install app (or Add to Home screen). Firefox and Edge work too.
   - Computer: use the install icon in the address bar of Chrome or Edge.
2. In Hearth, open Inbox, then Notifications on this device, and tap Turn on notifications. Allow the prompt.
3. Tap Send a test notification. It should arrive within a few seconds.
4. Under What rings, untick anything you do not want. The choices are Assigned to me, Mentions, Comments, Changes, Due soon, and Approvals. Every one is a notification Hearth already puts in your inbox; push adds nothing new.

Each device is turned on separately, and Your other devices lists the others so you can test or remove them. Quiet hours (set above, in the same Inbox settings) hold pushes as they hold email. The notification is still in your inbox when the quiet ends.

A notification shows one line, the same sentence as the inbox ("Sam assigned you Replace the outboard bearing"), and nothing from inside the item. Tapping it opens the item, after you sign in if your session has ended.

## For whoever runs Hearth

Nothing to add. The sender is part of the Hearth process.

- The VAPID key (the identity Hearth signs pushes with) is made once and kept. On the Helm chart it is a Secret named `<release>-vapid`, created on first install, kept when the release is removed (`helm.sh/resource-policy: keep`) and read back on every upgrade. On the Quadlet install Hearth makes it on first start and keeps it in the database, sealed with `HEARTH_ENCRYPTION_KEY`. Do not delete it while people have notifications on: each person would have to turn them on again.
- Set `HEARTH_PUSH_SUBJECT` (chart: `push.subject`) to a `mailto:` or `https:` contact. The push services see it. It defaults to the public base URL.
- A pipeline that renders the chart with `helm template` has no cluster to look in and would make a new key every render. Create the Secret yourself (a P-256 key in PEM under the key `key.pem`) and name it in `push.vapid.existingSecret`.
- `HEARTH_VAPID_KEY_FILE` points at a PEM key file and wins over the database copy. The chart sets it.
- Hearth must be able to make outbound HTTPS connections on port 443 to the push services: `fcm.googleapis.com` and numbered `*.google.com/fcm/` hosts (Chrome and Opera on Android and desktop), `*.push.services.mozilla.com` (Firefox), `*.push.apple.com` (Safari and iOS), and `*.notify.windows.com` (Edge). A subscription naming any other host is refused. Behind an egress proxy, allow those names.
- The page must be served over HTTPS, as browsers require for a service worker and for push.
- A push that the push service answers with 404 or 410 means the person removed the app or withdrew permission, and the device is forgotten. Other answers are kept as the device's last status and the device stays.
- Pushes are best effort and are never part of the change that caused them. The inbox row is the record.
