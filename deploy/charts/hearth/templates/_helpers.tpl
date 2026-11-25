{{/*
Expand the name of the chart.
*/}}
{{- define "hearth.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "hearth.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart name and version, for the helm.sh/chart label.
*/}}
{{- define "hearth.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "hearth.labels" -}}
helm.sh/chart: {{ include "hearth.chart" . }}
{{ include "hearth.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels. Deliberately NOT including component, so a caller can add it.
*/}}
{{- define "hearth.selectorLabels" -}}
app.kubernetes.io/name: {{ include "hearth.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Selector labels for one component. Pass a dict of root and component.

Every workload here is selected by name, instance, and component together,
because one release runs three distinct things and a selector is immutable once
the object exists. Getting this wrong is not a rename, it is a delete and
recreate.
*/}}
{{- define "hearth.componentSelectorLabels" -}}
{{- include "hearth.selectorLabels" .root }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{/*
Names for the two stateful components.

These default to bare "postgres" and "minio" rather than a release-prefixed name,
and that is deliberate. A StatefulSet's name decides its PVC names, and a PVC name
cannot be changed. Renaming these to hearth-postgres would not migrate anything,
it would silently provision empty volumes beside the full ones and come up as a
brand new database. The namespace belongs to this one release, so there is nothing
to collide with.
*/}}
{{- define "hearth.postgres.fullname" -}}
{{- default "postgres" .Values.postgresql.fullnameOverride }}
{{- end }}

{{/*
Bare "opensearch", for the same reason minio and postgres are bare: a
StatefulSet name decides its PVC names, and a PVC name cannot be changed.
Prefixing this later would not migrate an index, it would provision an empty
volume beside the full one and come up with nothing in it.
*/}}
{{- define "hearth.opensearch.fullname" -}}
{{- default "opensearch" .Values.opensearch.fullnameOverride }}
{{- end }}

{{- define "hearth.minio.fullname" -}}
{{- default "minio" .Values.minio.fullnameOverride }}
{{- end }}

{{/*
Which store the app and the backup job use, and where they reach an S3 API, as
host:port with no scheme: the Garage beside the app unless s3.endpoint says
otherwise, which is how a release points at some other S3 API.
*/}}
{{- define "hearth.s3.backend" -}}
{{- $b := .Values.s3.backend | default "garage" -}}
{{- if not (has $b (list "garage" "minio" "s3" "azure" "gcs")) -}}
{{- fail (printf "s3.backend must be garage, minio, s3, azure or gcs, not %q" $b) -}}
{{- end -}}
{{- if and (eq $b "garage") (not .Values.garage.enabled) (not .Values.s3.endpoint) -}}
{{- fail "s3.backend is garage but garage.enabled is false: there is no Garage to point at" -}}
{{- end -}}
{{- if and (eq $b "s3") (not .Values.s3.endpoint) -}}
{{- fail "s3.backend is s3 but s3.endpoint is empty: say which S3 API to use" -}}
{{- end -}}
{{- $b -}}
{{- end }}

{{/*
Which store the application is told to use, as its own HEARTH_STORAGE_BACKEND
value. An s3.endpoint means some other S3 API, whatever s3.backend says. The
chart's MinIO is an S3 API too.
*/}}
{{- define "hearth.storage.kind" -}}
{{- $b := include "hearth.s3.backend" . -}}
{{- if or (eq $b "azure") (eq $b "gcs") -}}
{{- $b -}}
{{- else if or .Values.s3.endpoint (eq $b "s3") (eq $b "minio") -}}
s3
{{- else -}}
garage
{{- end -}}
{{- end }}

{{- define "hearth.s3.endpoint" -}}
{{- if .Values.s3.endpoint -}}
{{- .Values.s3.endpoint -}}
{{- else if eq (include "hearth.s3.backend" .) "garage" -}}
{{- printf "%s-garage:3900" .Release.Name -}}
{{- else -}}
{{- printf "%s:9000" (include "hearth.minio.fullname" .) -}}
{{- end -}}
{{- end }}

{{/*
The object store's keys, and on Garage its region, as env entries. One place,
so the app and the backup job read the same store with the same credentials.
*/}}
{{- define "hearth.s3.credentialEnv" -}}
{{- if and (not .Values.s3.endpoint) (eq (include "hearth.s3.backend" .) "garage") -}}
{{- $secret := printf "%s-garage-key-%s" .Release.Name .Values.s3.garage.key -}}
- name: HEARTH_S3_ACCESS_KEY
  valueFrom:
    secretKeyRef:
      name: {{ $secret }}
      key: S3_ACCESS_KEY_ID
- name: HEARTH_S3_SECRET_KEY
  valueFrom:
    secretKeyRef:
      name: {{ $secret }}
      key: S3_SECRET_ACCESS_KEY
# Garage refuses a request signed for any region but its own.
- name: HEARTH_S3_REGION
  valueFrom:
    secretKeyRef:
      name: {{ $secret }}
      key: S3_REGION
{{- else -}}
- name: HEARTH_S3_ACCESS_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.existingSecret }}
      key: {{ .Values.secretKeys.s3AccessKey }}
- name: HEARTH_S3_SECRET_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.existingSecret }}
      key: {{ .Values.secretKeys.s3SecretKey }}
{{- if .Values.s3.endpoint }}
{{- if .Values.s3.region }}
- name: HEARTH_S3_REGION
  value: {{ .Values.s3.region | quote }}
{{- end }}
{{- if .Values.s3.useSSL }}
- name: HEARTH_S3_USE_SSL
  value: "true"
{{- end }}
{{- if .Values.s3.pathStyle }}
- name: HEARTH_S3_PATH_STYLE
  value: "true"
{{- end }}
{{- end }}
{{- end -}}
{{- end }}

{{/*
The image reference. The tag is an immutable git sha in every real deploy.
*/}}
{{- define "hearth.image" -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default (printf "sha-%s" .Chart.AppVersion)) }}
{{- end }}

{{/*
True when the chart should send plain http to https for this install: the ingress
is Traefik's, it terminates TLS, and the site is declared https. Over http the
browser sends no Sec-Fetch-Site, so the same-origin check refuses every sign-in;
a redirect means nobody ever signs in over http.
*/}}
{{- define "hearth.httpsRedirect" -}}
{{- if and .Values.ingress.enabled .Values.ingress.httpsRedirect.enabled .Values.ingress.tls.enabled (eq (.Values.ingress.className | default "") "traefik") (hasPrefix "https://" (.Values.hearth.publicBaseUrl | default "")) -}}
true
{{- end -}}
{{- end }}

{{/* Name of the redirect Middleware. Traefik refers to it as <namespace>-<name>@kubernetescrd. */}}
{{- define "hearth.httpsRedirectName" -}}
{{- printf "%s-https-redirect" (include "hearth.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/*
The database connection, as env entries, for the app and every job that talks to
Postgres. Three ways in, one place:

  postgresql.enabled         the Postgres this chart runs
  externalDatabase.dsnKey    a full DSN read from existingSecret, for any server
  externalDatabase.host ...  host, port, user, database and sslmode, with the
                             password from existingSecret (secretKeys.postgresPassword)

The DSN is assembled in the pod from the password, so the credential exists once and
never lands in a ConfigMap.
*/}}
{{/*
The Secret the Postgres password is read from: postgresPasswordSecret when it is set, else existingSecret, so a chart
that sets nothing reads what it always read. Postgres itself, the app and every job read it from the same place.
*/}}
{{- define "hearth.postgres.passwordSecret" -}}
{{- .Values.postgresPasswordSecret | default .Values.existingSecret -}}
{{- end }}
{{- define "hearth.database.env" -}}
{{- if .Values.postgresql.enabled -}}
- name: POSTGRES_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "hearth.postgres.passwordSecret" . }}
      key: {{ .Values.secretKeys.postgresPassword }}
- name: HEARTH_DATABASE_URL
  value: postgres://{{ .Values.postgresql.user }}:$(POSTGRES_PASSWORD)@{{ include "hearth.postgres.fullname" . }}:5432/{{ .Values.postgresql.database }}?sslmode=disable
{{- else if .Values.externalDatabase.dsnKey -}}
- name: HEARTH_DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ .Values.existingSecret }}
      key: {{ .Values.externalDatabase.dsnKey }}
{{- else if .Values.externalDatabase.host -}}
- name: POSTGRES_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "hearth.postgres.passwordSecret" . }}
      key: {{ .Values.secretKeys.postgresPassword }}
- name: HEARTH_DATABASE_URL
  value: postgres://{{ .Values.externalDatabase.user }}:$(POSTGRES_PASSWORD)@{{ .Values.externalDatabase.host }}:{{ .Values.externalDatabase.port }}/{{ .Values.externalDatabase.database }}?sslmode={{ .Values.externalDatabase.sslmode }}
{{- else -}}
{{- fail "postgresql.enabled is false, so set externalDatabase.host (or externalDatabase.dsnKey) to say where Postgres is" -}}
{{- end -}}
{{- end }}

{{/* host and port the pod waits for before it starts, or nothing when the DSN hides them */}}
{{- define "hearth.database.waitTarget" -}}
{{- if .Values.postgresql.enabled -}}
{{- printf "%s 5432" (include "hearth.postgres.fullname" .) -}}
{{- else if and .Values.externalDatabase.host (not .Values.externalDatabase.dsnKey) -}}
{{- printf "%s %v" .Values.externalDatabase.host .Values.externalDatabase.port -}}
{{- end -}}
{{- end }}

{{/*
Everything the app and the backup job are told about object storage, in one
place so the two can never point at different stores. The S3 family (Garage, the
chart's MinIO, any other S3 API) keeps the settings it always had; Azure Blob and
Google Cloud Storage have their own.
*/}}
{{- define "hearth.storage.env" -}}
{{- $b := include "hearth.s3.backend" . -}}
- name: HEARTH_STORAGE_BACKEND
  value: {{ include "hearth.storage.kind" . | quote }}
{{- if eq $b "azure" }}
- name: HEARTH_STORAGE_BUCKET
  value: {{ .Values.azure.container | default .Values.s3.bucket | quote }}
- name: HEARTH_AZURE_ACCOUNT
  value: {{ required "azure.account is required when s3.backend is azure" .Values.azure.account | quote }}
{{- if .Values.azure.endpoint }}
- name: HEARTH_AZURE_ENDPOINT
  value: {{ .Values.azure.endpoint | quote }}
{{- end }}
- name: HEARTH_AZURE_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.existingSecret }}
      key: {{ .Values.secretKeys.azureKey }}
{{- else if eq $b "gcs" }}
- name: HEARTH_STORAGE_BUCKET
  value: {{ .Values.gcs.bucket | default .Values.s3.bucket | quote }}
{{- if .Values.gcs.credentialsKey }}
- name: HEARTH_GCS_CREDENTIALS_JSON
  valueFrom:
    secretKeyRef:
      name: {{ .Values.existingSecret }}
      key: {{ .Values.gcs.credentialsKey }}
{{- end }}
{{- if .Values.gcs.endpoint }}
- name: HEARTH_GCS_ENDPOINT
  value: {{ .Values.gcs.endpoint | quote }}
{{- end }}
{{- else }}
- name: HEARTH_S3_ENDPOINT
  value: {{ include "hearth.s3.endpoint" . | quote }}
- name: HEARTH_S3_PUBLIC_ENDPOINT
  value: {{ .Values.s3.publicEndpoint | quote }}
- name: HEARTH_S3_BUCKET
  value: {{ .Values.s3.bucket | quote }}
{{ include "hearth.s3.credentialEnv" . }}
{{- end }}
{{- end }}

{{/* Labels on the auto-upgrade's objects. No chart version in them, so an upgrade does not rewrite a cluster-scoped object for a label. */}}
{{- define "hearth.autoupgrade.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: autoupgrade
{{- end }}
