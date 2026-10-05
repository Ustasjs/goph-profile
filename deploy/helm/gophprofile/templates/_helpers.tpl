{{/*
Chart name, allowing the usual override.
*/}}
{{- define "gophprofile.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/*
Fully qualified name: release name, or release-chart when they differ.
*/}}
{{- define "gophprofile.fullname" -}}
{{- if contains (include "gophprofile.name" .) .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "gophprofile.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end }}

{{/*
Common labels for every resource.
*/}}
{{- define "gophprofile.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "gophprofile.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels; the caller appends the component label.
*/}}
{{- define "gophprofile.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gophprofile.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
In-cluster service names of the bundled infrastructure.
*/}}
{{- define "gophprofile.postgresHost" -}}
{{ include "gophprofile.fullname" . }}-postgres
{{- end }}
{{- define "gophprofile.minioHost" -}}
{{ include "gophprofile.fullname" . }}-minio
{{- end }}
{{- define "gophprofile.rabbitmqHost" -}}
{{ include "gophprofile.fullname" . }}-rabbitmq
{{- end }}

{{/*
Database DSN: assembled from postgresql.auth when the bundled
PostgreSQL is enabled (single source of truth for the password),
otherwise taken verbatim from secrets.databaseDSN.
*/}}
{{- define "gophprofile.databaseDSN" -}}
{{- if .Values.postgresql.enabled -}}
postgres://{{ .Values.postgresql.auth.username }}:{{ .Values.postgresql.auth.password }}@{{ include "gophprofile.postgresHost" . }}:5432/{{ .Values.postgresql.auth.database }}?sslmode=disable
{{- else -}}
{{- required "secrets.databaseDSN is required when postgresql.enabled is false" .Values.secrets.databaseDSN -}}
{{- end -}}
{{- end }}

{{/*
AMQP URL: same single-source rule as the database DSN.
*/}}
{{- define "gophprofile.rabbitmqURL" -}}
{{- if .Values.rabbitmq.enabled -}}
amqp://{{ .Values.rabbitmq.auth.username }}:{{ .Values.rabbitmq.auth.password }}@{{ include "gophprofile.rabbitmqHost" . }}:5672/
{{- else -}}
{{- required "secrets.rabbitmqURL is required when rabbitmq.enabled is false" .Values.secrets.rabbitmqURL -}}
{{- end -}}
{{- end }}

{{/*
S3 credentials: the bundled MinIO root account doubles as the app's
S3 account, so enabling minio overrides secrets.s3AccessKey/SecretKey.
*/}}
{{- define "gophprofile.s3AccessKey" -}}
{{- if .Values.minio.enabled -}}
{{- .Values.minio.auth.rootUser -}}
{{- else -}}
{{- required "secrets.s3AccessKey is required when minio.enabled is false" .Values.secrets.s3AccessKey -}}
{{- end -}}
{{- end }}
{{- define "gophprofile.s3SecretKey" -}}
{{- if .Values.minio.enabled -}}
{{- .Values.minio.auth.rootPassword -}}
{{- else -}}
{{- required "secrets.s3SecretKey is required when minio.enabled is false" .Values.secrets.s3SecretKey -}}
{{- end -}}
{{- end }}
{{- define "gophprofile.s3Endpoint" -}}
{{- if .Values.minio.enabled -}}
{{ include "gophprofile.minioHost" . }}:9000
{{- else -}}
{{- required "config.s3Endpoint is required when minio.enabled is false" .Values.config.s3Endpoint -}}
{{- end -}}
{{- end }}
