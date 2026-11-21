# Azure Blob Storage

Hearth uses the official Azure SDK for Go. Files go into one container in one storage account, authenticated with the account key.

## Settings

| Variable | Value |
|---|---|
| `HEARTH_STORAGE_BACKEND` | `azure` |
| `HEARTH_AZURE_ACCOUNT` | The storage account name |
| `HEARTH_AZURE_KEY` | The account's shared key |
| `HEARTH_AZURE_ENDPOINT` | The blob service URL. Empty means `https://<account>.blob.core.windows.net`. Set it for a sovereign cloud, a private endpoint, or Azurite. |
| `HEARTH_STORAGE_BUCKET` | The container name. Hearth creates it if it does not exist. |

## Helm

```yaml
s3:
  backend: azure
azure:
  account: myaccount
  container: hearth
```

Put the account key in the Secret named by `existingSecret`, under `AZURE_STORAGE_KEY`. The bundled Garage can be turned off with `garage.enabled: false`.

## Presigned URLs

Downloads and uploads can use a SAS URL signed with the account key, valid for the time Hearth asks for and for that one blob. A browser upload to a SAS URL must send the header `x-ms-blob-type: BlockBlob`.

## Testing

The suite runs against Azurite, Microsoft's emulator, with its published development account. A real storage account has not been used for these tests.
