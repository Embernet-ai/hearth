# Hearth release checklist

Written from how Hearth is actually built and deployed, and from IRON's checklist as the closest Go service. Follow it in order and read each result back from the system. A green run proves the workflow ran, not that the thing serves.

## 1. Before the tag

1. `main` is the commit you mean to release, CI is green on it, and the Publish run for that commit (the `sha-` image) is green.
2. The version is the same in three places: `deploy/charts/hearth/Chart.yaml` `version` and `appVersion`, and `deploy/charts/hearth/values.yaml` `image.tag`.
3. A new migration is in `migrations/` with the next number, and nothing else has taken it.

## 2. The tag

Only the lead pushes it: `git tag -a vX.Y.Z -m "Hearth X.Y.Z"` then `git push origin vX.Y.Z`. The Publish workflow builds the image and pushes `:X.Y.Z` and `:sha-<short>`. A tag builds `linux/amd64` only. The multi-arch image (adding `linux/arm64` and the qemu step to `publish.yml`) is one small bump release behind it, after the amd64 release is live and reads stable.

## 3. Deploy (Helm, never a patch of the Deployment)

1. Copy `deploy/charts/hearth` to the cluster and read the live release: `helm -n hearth get manifest hearth`.
2. Server-side dry run with the new tag: `helm upgrade hearth ./hearth --reset-then-reuse-values --set image.tag=X.Y.Z --dry-run=server`, and diff the manifest against the live one. Only version labels, the image tag, and `HEARTH_VERSION` should change, plus anything the release meant to add.
3. `helm upgrade hearth ./hearth --reset-then-reuse-values --set image.tag=X.Y.Z --timeout 8m --wait`.

## 4. Read it back

1. The pod log shows the schema migration (`schema migrated`, from and to) and `listening`.
2. `/healthz` reports the release revision and `profile: release`.
3. The things the release changed, read on the live system: counts, the page, the job's report. Not a status word.
4. The CronJobs (`hearth-backup`, and `hearth-crm-sync` when `crmSync.enabled`) are listed with the new image on their next run.
5. Anything a one-off Job left behind is deleted, with its cause named if it failed.
