# Garage

Garage is the default, and the reason Hearth needs no cloud account. It is a small S3-compatible object store that I run beside Hearth, on the same cluster or the same box. Your files live on your own volumes.

## Helm

Nothing to set. The chart depends on the Garage chart, starts a single node with a 20 Gi data volume, creates the `hearth` bucket and a key that can read and write only that bucket, and points the application and the backup job at it. The key is made before the application pods start, so a new release never waits on a credential that does not exist yet.

```
helm install hearth deploy/charts/hearth --set ingress.host=hearth.example.com
```

To change the size of the volume, set `garage.garage.persistence.data.size`.

## Podman and Quadlet

`deploy/quadlet/install.sh` starts Garage as a rootless Quadlet service beside Hearth and Postgres, runs its one-time layout and bucket setup, and stores the access key as a Podman secret. Run it again and it keeps what it already made.

## Settings

| Variable | Value |
|---|---|
| `HEARTH_STORAGE_BACKEND` | `garage` |
| `HEARTH_S3_ENDPOINT` | `hearth-garage:3900`, host and port with no scheme |
| `HEARTH_S3_PUBLIC_ENDPOINT` | Any name; it must differ from the endpoint in production |
| `HEARTH_S3_ACCESS_KEY`, `HEARTH_S3_SECRET_KEY` | The key Garage made |
| `HEARTH_S3_REGION` | `garage`. Garage refuses a request signed for any other region. |

Garage answers path-style requests only, and Hearth sets that for you.

## Backups

`hearth-backup` archives the database and every object in the bucket, nightly. `hearthctl verify` checks an archive and `hearthctl drill` restores one into a spare database and a spare bucket.
