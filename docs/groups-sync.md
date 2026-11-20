# Google Workspace groups follow Hearth

`hearthctl groups-sync` makes the members of Google Workspace groups follow the teams and departments people are assigned to in Hearth.

```
hearthctl groups-sync -config groups.json            # what would change, writes nothing
hearthctl groups-sync -config groups.json -apply     # make it so
```

## Config

The mapping is a file the operator owns, so no group address lives in the code.

```json
{
  "mappings": [
    {"group": "crew@example.com", "teams": ["Crew"], "departments": ["Operations"]}
  ],
  "protect": ["admin@example.com"]
}
```

A group's wanted members are the active people in any listed team or department, by the email on their sign-in identity. A mapping with no team and no department is refused, never read as "empty the group".

## Rules it keeps

- Only plain members are added or removed. Owners and managers are set by hand in the directory and are never removed here.
- An address on `protect` is never removed from any group.
- A suspended account is not wanted, so it comes off the group, the same as it comes off its teams.
- Dry run is the default.

## Credentials

`HEARTH_GOOGLE_GROUPS_KEY_FILE` is a service account key file and `HEARTH_GOOGLE_GROUPS_SUBJECT` is the admin it acts as, through domain-wide delegation. The one scope it asks for is `https://www.googleapis.com/auth/admin.directory.group.member`, which has to be delegated to that service account in the Admin console. Without it Google answers `unauthorized_client` and the command says which scope is missing.

Run it as a one-off Job like `deploy/jobs/crm-sync.yaml`, or on a schedule.
