# Where Hearth keeps your files

Hearth keeps every attachment, photo, and document upload in object storage, and it keeps them on your own hardware unless you decide otherwise. I built it that way on purpose: nobody should need a cloud subscription to hold their own work.

Out of the box, a self-hosted install runs on [Garage](garage.md), an S3-compatible store that ships with Hearth, in the Helm chart and in the Podman and Quadlet install alike. There is nothing to sign up for and nothing to configure. If you would rather keep your files somewhere else, Hearth speaks to four backends, chosen by one setting:

| Backend | `HEARTH_STORAGE_BACKEND` | Use it when | Page |
|---|---|---|---|
| Garage | `garage` (the default) | You host Hearth yourself and want your data on your own disks. | [garage.md](garage.md) |
| S3-compatible | `s3` | You already run an S3 API: AWS S3, Wasabi, Backblaze B2, Cloudflare R2, or a MinIO of your own. | [s3.md](s3.md) |
| Azure Blob Storage | `azure` | Your company keeps its data in Azure. | [azure.md](azure.md) |
| Google Cloud Storage | `gcs` | Your company keeps its data in Google Cloud. | [gcs.md](gcs.md) |

Moving between them is a copy, not a rebuild. [Migrating](migrating.md) covers `hearthctl blob-migrate`, which copies every object, reads each one back, compares SHA-256, and counts both sides before you switch anything.

## Settings common to every backend

| Variable | Meaning |
|---|---|
| `HEARTH_STORAGE_BACKEND` | `garage`, `s3`, `azure`, or `gcs`. Empty means `garage`. |
| `HEARTH_STORAGE_BUCKET` | The bucket, or on Azure the container. Defaults to `hearth`. `HEARTH_S3_BUCKET`, the older name, still works. |

Each backend page lists the settings that are its own. In the Helm chart the same choice is `s3.backend`, with the `s3`, `azure`, and `gcs` blocks in `values.yaml`.

## What a backend must do

Every backend passes the same suite: a file goes in and comes back with the same SHA-256, large and unknown-length uploads work, a missing object is reported as missing, listing is complete, deleting twice is not an error, and, where the service supports it, a presigned download and upload work and a damaged signature is refused. The suite runs against Garage, a MinIO as the generic S3 case, Microsoft's Azurite emulator, and fake-gcs-server for Google, on every change.

## Presigned URLs

Where a backend can sign a URL, a browser can download or upload straight from the store, and large files never pass through Hearth. Garage, S3, and Azure always can. Google can when it has a service account key; with only the machine's own credentials it says so and Hearth streams downloads instead. Today Hearth streams downloads through the application on every backend, because that is where access is enforced.
