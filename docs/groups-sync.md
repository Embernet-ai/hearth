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

Where the Google organization forbids service account keys, run it keyless, the way the mail relay does. Set `HEARTH_GOOGLE_GROUPS_VORD_TOKEN_URL`, `HEARTH_GOOGLE_GROUPS_CLIENT_ID`, `HEARTH_GOOGLE_GROUPS_CLIENT_SECRET`, `HEARTH_GOOGLE_GROUPS_AUDIENCE` (the workload identity provider), `HEARTH_GOOGLE_GROUPS_SERVICE_ACCOUNT` (the account that holds the delegation) and `HEARTH_GOOGLE_GROUPS_SUBJECT` (the admin it acts as). The command gets a Vord token, trades it at Google's STS, has IAM sign the delegation assertion as the service account, and trades that for a Directory token. The federated identity needs `roles/iam.serviceAccountTokenCreator` on the service account.

Elsewhere, `HEARTH_GOOGLE_GROUPS_KEY_FILE` plus `HEARTH_GOOGLE_GROUPS_SUBJECT` use a service account key file. For a one hour run by a person, `HEARTH_GOOGLE_GROUPS_ACCESS_TOKEN` is a ready Directory token and is used as is.

The one scope it asks for is `https://www.googleapis.com/auth/admin.directory.group.member`, which has to be delegated to the service account in the Admin console. Without it Google answers `unauthorized_client` and the command says which scope is missing.

## On a schedule

The chart's `groupsSync` values render a ConfigMap with the mapping and a CronJob that runs `groups-sync -apply` (set `apply: false` for a run that only logs). A one-off Job like `deploy/jobs/crm-sync.yaml` works too.
