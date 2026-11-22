# Releasing Hearth

One version number, moved in one commit, tagged once. That is the whole rule, and
it exists because the alternative was tried and it did not survive three releases.

## What went wrong before 1.0.0

The chart used to carry two numbers. `version` tracked the packaging and moved on
every change, `appVersion` tracked the binary and moved only when Go code moved.
That is defensible, it is what Helm's own documentation suggests, and it produced
0.3.2, 0.3.3 and 0.4.0: three chart versions, all sitting on `appVersion: "0.3.1"`,
none of them tagged, and no image published for any of them.

Nothing was broken by it. Every one of those commits said plainly that it was
chart-only and that the app version was staying put, and every one of them was
right. The cost was not a bug, it was that answering "what is actually deployed"
took reading three commit bodies, and that no release note existed for any of it
because there was no tag to hang one on.

1.0.0 collapses the two numbers into one. A chart-only change now republishes an
image identical to the last one, which is a few seconds of CI and some bytes in
GHCR. That is a cheaper thing to pay than the ambiguity was.

## The checklist

Work through it in order. Every step is one command and none of them are optional.

**1. Start from a green main.**

```bash
git checkout main && git pull --ff-only
go build ./... && go vet ./... && go test ./...
```

CI runs the integration tests against a real Postgres, a real MinIO and a real
OpenSearch, which a laptop usually does not have. A local run that passes is
necessary and not sufficient, so check that the last CI run on main was green too.

**2. Move the version in all three places.**

| File | Field |
|---|---|
| `deploy/charts/hearth/Chart.yaml` | `version` |
| `deploy/charts/hearth/Chart.yaml` | `appVersion` |
| `deploy/charts/hearth/values.yaml` | `image.tag` |

All three are the same number, and there is nowhere else in the repository that
carries a version. Confirm that:

```bash
grep -rn "$OLD" --include="*.yaml" --include="*.go" --include="*.md" .
```

`image.tag` is the one that decides what runs. `appVersion` is documentation and
`version` is what Helm resolves, but a deploy that does not change `image.tag`
does not change the Deployment spec, so Kubernetes has no reason to act and the
pod keeps serving old code while every probe reports healthy. That has happened
here once already.

**3. Render the chart before committing.**

```bash
helm lint deploy/charts/hearth
helm template hearth deploy/charts/hearth | grep -E "image:|appVersion"
```

**4. Commit, with the release note in the body.**

The commit message is the release note. It gets copied onto the tag and onto the
GitHub release, so write it once and write it properly: what changed, why it
needed changing, and how it was verified. Subject line is `<version>: <what was
wrong>`, lowercase after the colon.

**5. Tag it, annotated, with the same note.**

```bash
git tag -a v1.0.0 -F <(git log -1 --format=%B)
git push origin main
git push origin v1.0.0
```

Tags before 1.0.0 carry only the bare version string in the tag body. That is why
none of them produced a release note without going back to the commit. Do not
repeat it.

**Platforms.** `RELEASE_PLATFORMS` holds `amd64`. A tag builds the amd64 image only; the multi-arch image comes in a second, small bump release behind it.

**6. Watch the publish.**

```bash
gh run watch $(gh run list --workflow=publish.yml --limit 1 --json databaseId -q '.[0].databaseId')
```

`publish.yml` builds on a `v*` tag and publishes `ghcr.io/embernet-ai/hearth-community` under two names for the same image, the
version and `sha-<short>`, so a values file pinned to either keeps resolving. There
is no `:latest` and there is never going to be one.

**7. Write the GitHub release.**

```bash
gh release create v1.0.0 --title "v1.0.0" --notes-file <(git tag -l --format='%(contents)' v1.0.0)
```

**8. Deploy, and check what actually landed.**

```bash
helm upgrade --install hearth deploy/charts/hearth -n hearth -f my-values.yaml --set image.tag=1.0.0
kubectl -n hearth get deploy hearth -o jsonpath='{.spec.template.spec.containers[0].image}'
kubectl -n hearth rollout status deploy/hearth
```

The last two matter more than the first. A `helm upgrade` that reports success and
leaves the old image running is the exact failure this whole document is arranged
around, so read the image back off the running Deployment rather than trusting the
upgrade to have done what it said.

**9. Prove the backup still works against the new schema.**

```bash
kubectl -n hearth create job --from=cronjob/hearth-backup backup-postrelease-1-0-0
```

A migration that lands cleanly and breaks the backup is a release that has taken
away the ability to undo itself. This is the cheapest possible check and it has
caught real problems: the backup job failed silently for its entire existence
before 0.3.2, and nothing noticed because `concurrencyPolicy: Forbid` suppressed
every run behind the first wedged one.
