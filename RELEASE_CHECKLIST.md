# Hearth release checklist

Written from how Hearth is actually built and deployed, and from IRON's checklist as the closest Go service. Follow it in order and read each result back from the system. A green run proves the workflow ran, not that the thing serves.

## 1. Before the tag

1. `main` is the commit you mean to release, CI is green on it, and the Publish run for that commit (the `sha-` image) is green.
2. The version is the same in three places: `deploy/charts/hearth/Chart.yaml` `version` and `appVersion`, and `deploy/charts/hearth/values.yaml` `image.tag`.
3. A new migration is in `migrations/` with the next number, and nothing else has taken it.

## 2. The tag, and the image

Only the lead pushes it: `git tag -a vX.Y.Z -m "Hearth X.Y.Z"` then `git push origin vX.Y.Z`.

**The tag does not publish an image.** The Image build workflow here (`publish.yml`) only builds, to catch a broken Containerfile, and pushes nothing: the package `ghcr.io/embernet-ai/hearth` can be written only by the repository it is linked to, `Embernet-ai/hearth-original`. Publishing is that repository's `relay.yml`, and the lead dispatches it after the tag:

```sh
gh workflow run relay.yml -R Embernet-ai/hearth-original -f tag=vX.Y.Z
gh run watch -R Embernet-ai/hearth-original $(gh run list -R Embernet-ai/hearth-original --workflow relay.yml --limit 1 --json databaseId -q '.[0].databaseId')
```

It builds `linux/amd64` only and pushes `:X.Y.Z`, `:sha-<short>`, and `:stable` when the tag is the newest. The multi-arch image (adding `linux/arm64` and the qemu step) is one small bump release behind it, after the amd64 release is live and reads stable. A slow schedule in the same workflow builds any release tag that has no image, as a safety net, not as the way to publish.

**Before any roll, read the tag back from the registry**, so a roll never points a Deployment at an image that does not exist (2.23.1 was rolled before its image was published, and the roll failed on a pull, leaving the old pod serving):

```sh
gh api "/orgs/Embernet-ai/packages/container/hearth/versions?per_page=100" --jq '.[].metadata.container.tags[]' | grep -x X.Y.Z
```

No line printed means no image yet. Do not run `helm upgrade` until it prints the version. The auto-upgrade CronJob (`deploy/autoupgrade/`) makes the same check for itself and waits.

## 3. The GitHub release

Right after the tag, write the release on this repository, from the tag's own note, and let it be Latest: `gh release create vX.Y.Z --verify-tag --title "vX.Y.Z" --notes-file <(git tag -l --format='%(contents)' vX.Y.Z)`. Then read `gh api repos/Embernet-ai/hearth/releases/latest --jq .html_url` and check it names the version you just tagged. A tag with no release leaves the releases page behind the code, which is how v2.22.3 through v2.22.10 went unannounced.

## 4. Deploy (Helm, never a patch of the Deployment)

1. Copy `deploy/charts/hearth` to the cluster and read the live release: `helm -n hearth get manifest hearth`.
2. Server-side dry run with the new tag: `helm upgrade hearth ./hearth --reset-then-reuse-values --set image.tag=X.Y.Z --dry-run=server`, and diff the manifest against the live one. Only version labels, the image tag, and `HEARTH_VERSION` should change, plus anything the release meant to add.
3. `helm upgrade hearth ./hearth --reset-then-reuse-values --set image.tag=X.Y.Z --timeout 8m --wait`.

## 5. Read it back

1. The pod log shows the schema migration (`schema migrated`, from and to) and `listening`.
2. `/healthz` reports the release revision and `profile: release`.
3. The things the release changed, read on the live system: counts, the page, the job's report. Not a status word.
4. The CronJobs (`hearth-backup`, `hearth-crm-sync` when `crmSync.enabled`, and `hearth-groups-sync` when `groupsSync.enabled`) are listed with the new image on their next run.
5. Anything a one-off Job left behind is deleted, with its cause named if it failed.
