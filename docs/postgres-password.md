# The Postgres password from its own Secret

`postgresPasswordSecret` names a Secret that holds only the Postgres password, under `secretKeys.postgresPassword`. It exists for an install that makes that one credential from code (Terraform, a secrets operator) and keeps the rest of `existingSecret` as it is. Empty, which is the default, reads the password from `existingSecret` exactly as before.

* The app and the backup, crm-sync, mautic-sync and groups-sync jobs read the password from it, and build the database URL in the pod from it.
* The bundled Postgres reads `POSTGRES_PASSWORD` only when it first creates its data directory. A new install sets `postgresql.passwordSecret` to the same Secret so the database starts with that password. An existing database leaves it alone, because changing the StatefulSet's Secret would restart the database for nothing.

## Changing the password of a running database

The password lives in the database, so the Secret changing is not enough. Order matters:

1. `ALTER ROLE hearth PASSWORD ...` inside Postgres, with the new value read from the Secret in the cluster (never typed or printed).
2. Roll the app straight away (`kubectl rollout restart deploy/hearth`). The old pods keep the connections they have until they are replaced, so nothing drops.
3. Read back `/healthz`, a page, and one run of each job and a backup.
