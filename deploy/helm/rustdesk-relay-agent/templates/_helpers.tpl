{{/*
Expand the name of the chart.
*/}}
{{- define "rustdesk-relay-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "rustdesk-relay-agent.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "rustdesk-relay-agent.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "rustdesk-relay-agent.labels" -}}
helm.sh/chart: {{ include "rustdesk-relay-agent.chart" . }}
{{ include "rustdesk-relay-agent.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "rustdesk-relay-agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "rustdesk-relay-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "rustdesk-relay-agent.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "rustdesk-relay-agent.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the secret containing enrollment token
*/}}
{{- define "rustdesk-relay-agent.secretName" -}}
{{- if .Values.panel.existingSecret }}
{{- .Values.panel.existingSecret }}
{{- else }}
{{- include "rustdesk-relay-agent.fullname" . }}
{{- end }}
{{- end }}
