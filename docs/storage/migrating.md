# Moving files between stores

`hearthctl blob-migrate` copies every object from one store into another and proves it. It works between any two backends: MinIO to Garage, Garage to Azure, S3 to Google, and back.

The destination is the store Hearth is configured for. The source is read from the same settings with `HEARTH_SRC_` in place of `HEARTH_`, for example `HEARTH_SRC_STORAGE_BACKEND=s3`, `HEARTH_SRC_S3_ENDPOINT`, `HEARTH_SRC_S3_ACCESS_KEY`, and `HEARTH_SRC_S3_SECRET_KEY`.

## What it guarantees

* It never deletes anything, from either side. The source stays whole, so it is the fallback.
* It never overwrites an object the destination already holds. If a key exists on both sides with different bytes, that is reported as a conflict and left alone.
* After every write it reads the object back from the destination and compares SHA-256 with what it read from the source.
* At the end it lists both stores again and counts them. It prints `verified: true` only when every source key is in the destination with equal bytes.
* It exits non-zero if anything conflicted or failed, so a Job or a script cannot mistake a bad run for a good one.
* It can be run again. A second run finds everything already there and copies nothing.

Objects written to the destination since a switch (they exist only there) are counted and never touched.

## The order

1. Run it as a dry run, which is the default. It writes nothing. Read the counts.
2. Run it with `-apply`. Read `verified`.
3. Switch Hearth to the new store (`s3.backend` and its settings in the chart, or `HEARTH_STORAGE_BACKEND` and friends).
4. Run `-apply` once more. It copies anything uploaded between step 2 and the switch.
5. Read a few files through Hearth, run a backup, and read it back.
6. Only then turn the old store off. For MinIO that is `minio.enabled: false`, which stops the pod and leaves its volume in place.

## On Kubernetes

`deploy/jobs/blob-migrate.yaml` is a one-off Job for the common case, MinIO to the bundled Garage. Its header shows the two commands.

## Anywhere else

```
HEARTH_SRC_STORAGE_BACKEND=s3 HEARTH_SRC_S3_ENDPOINT=minio:9000 \
HEARTH_SRC_S3_PUBLIC_ENDPOINT=files.invalid HEARTH_SRC_S3_ACCESS_KEY=... HEARTH_SRC_S3_SECRET_KEY=... \
HEARTH_STORAGE_BACKEND=azure HEARTH_AZURE_ACCOUNT=... HEARTH_AZURE_KEY=... \
hearthctl blob-migrate -apply
```
