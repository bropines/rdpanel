{{/*
Expand the name of the chart.
It is deliberately NOT .Chart.Name: the chart was first published as
"rustdesk-relay-agent", and resource names and selector labels derive from
this name, so existing releases keep their Deployment selectors and PVCs.
*/}}
{{- define "rdpanel-relay-agent.name" -}}
{{- default "rustdesk-relay-agent" .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "rdpanel-relay-agent.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := include "rdpanel-relay-agent.name" . }}
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
{{- define "rdpanel-relay-agent.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "rdpanel-relay-agent.labels" -}}
helm.sh/chart: {{ include "rdpanel-relay-agent.chart" . }}
{{ include "rdpanel-relay-agent.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "rdpanel-relay-agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "rdpanel-relay-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "rdpanel-relay-agent.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "rdpanel-relay-agent.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the secret containing enrollment token
*/}}
{{- define "rdpanel-relay-agent.secretName" -}}
{{- if .Values.panel.existingSecret }}
{{- .Values.panel.existingSecret }}
{{- else }}
{{- include "rdpanel-relay-agent.fullname" . }}
{{- end }}
{{- end }}
