# Google Cloud Storage

Hearth uses the official Google Cloud Go client. Files go into one bucket.

## Settings

| Variable | Value |
|---|---|
| `HEARTH_STORAGE_BACKEND` | `gcs` |
| `HEARTH_STORAGE_BUCKET` | The bucket. It must already exist: bucket names are global, so Hearth never guesses a project and creates one. |
| `HEARTH_GCS_CREDENTIALS_JSON` or `HEARTH_GCS_CREDENTIALS_FILE` | A service account key as JSON, or the path to a file holding it. Leave both empty to use the machine's own credentials (Workload Identity, or a metadata server). |
| `HEARTH_GCS_ENDPOINT` | A GCS emulator, for testing only. |

Give the service account `roles/storage.objectUser` on the bucket.

## Helm

```yaml
s3:
  backend: gcs
gcs:
  bucket: my-hearth-files
  credentialsKey: GCS_CREDENTIALS_JSON   # a key in existingSecret; empty uses the pod's own credentials
garage:
  enabled: false
```

## Presigned URLs

With a service account key, Hearth can make V4 signed URLs for downloads and uploads. With only the machine's own credentials it cannot sign, says so, and downloads stream through Hearth.

## Testing

The suite runs against fake-gcs-server. Signed URLs are checked for shape (host, algorithm, expiry, signature, download name) and are not fetched from Google. A real Google bucket has not been used for these tests.
