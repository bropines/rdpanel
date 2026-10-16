{{/* vim: set filetype=mustache: */}}

{{/*
Base name for resource names and the app.kubernetes.io/name selector label.
It is deliberately NOT .Chart.Name: the chart was published as "rustdesk-panel"
up to 0.1.x, and StatefulSet selectors and volumeClaimTemplates are immutable,
so releases installed from the old chart must keep rendering the same names
after an upgrade to the "rdpanel" chart. New installs may set nameOverride.
*/}}
{{- define "rdpanel.baseName" -}}
{{- default "rustdesk-panel" .Values.nameOverride -}}
{{- end -}}

{{- define "rdpanel.name" -}}
{{- include "rdpanel.baseName" . | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Full name. Kept to 50 chars so suffixed names (-rendezvous-tcp, -headless) stay <= 63.
*/}}
{{- define "rdpanel.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- $name := include "rdpanel.baseName" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "rdpanel.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "rdpanel.selectorLabels" -}}
app.kubernetes.io/name: {{ include "rdpanel.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "rdpanel.labels" -}}
helm.sh/chart: {{ include "rdpanel.chart" . }}
{{ include "rdpanel.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: rustdesk
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/* metadata.annotations block content (commonAnnotations merged with extra dict passed as .extra) */}}
{{- define "rdpanel.annotations" -}}
{{- $a := merge (dict) (.extra | default dict) (.root.Values.commonAnnotations | default dict) -}}
{{- if $a -}}
annotations:
  {{- toYaml $a | nindent 2 }}
{{- end }}
{{- end -}}

{{- define "rdpanel.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "rdpanel.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "rdpanel.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag | toString) -}}
{{- end -}}
{{- end -}}

{{/* Panel HTTP port, derived from panel.listen (":21114", "127.0.0.1:31114", "[::]:21114"). */}}
{{- define "rdpanel.httpPort" -}}
{{- regexReplaceAll "^.*:" (toString .Values.panel.listen) "" -}}
{{- end -}}

{{- define "rdpanel.secretName" -}}
{{- default (include "rdpanel.fullname" .) .Values.secrets.existingSecret -}}
{{- end -}}

{{- define "rdpanel.serverKeysSecretName" -}}
{{- default (printf "%s-server-keys" (include "rdpanel.fullname" .)) .Values.serverKeys.existingSecret -}}
{{- end -}}

{{- define "rdpanel.websocketServiceEnabled" -}}
{{- if or .Values.websocket.service.enabled .Values.websocket.ingress.enabled (and .Values.httpRoute.enabled .Values.httpRoute.websocketRules) -}}
true
{{- end -}}
{{- end -}}

{{- define "rdpanel.dataClaimName" -}}
{{- if .Values.persistence.existingClaim -}}
{{- .Values.persistence.existingClaim -}}
{{- else -}}
{{- printf "data-%s-0" (include "rdpanel.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/* Env vars shared by the server container and the migration job. */}}
{{- define "rdpanel.env" -}}
- name: RUSTDESK_API_JWT_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "rdpanel.secretName" . }}
      key: {{ .Values.secrets.jwtKeyKey }}
{{- if .Values.secrets.setSecretKey }}
- name: RDPANEL_SECRET_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "rdpanel.secretName" . }}
      key: {{ .Values.secrets.secretKeyKey }}
{{- end }}
{{- end -}}

{{/* Generated probe handler. */}}
{{- define "rdpanel.probeHandler" -}}
{{- if eq .Values.probes.type "exec" }}
exec:
  command:
    - wget
    - -q
    - -O
    - /dev/null
    - -T
    - "3"
    - http://127.0.0.1:{{ include "rdpanel.httpPort" . }}{{ .Values.probes.path }}
{{- else }}
httpGet:
  path: {{ .Values.probes.path }}
  port: http
{{- end }}
{{- end -}}

{{- define "rdpanel.probe" -}}
{{- $p := .probe -}}
{{ include "rdpanel.probeHandler" .root | trim }}
{{- range $k := list "initialDelaySeconds" "periodSeconds" "timeoutSeconds" "failureThreshold" "successThreshold" }}
{{- if hasKey $p $k }}
{{ $k }}: {{ index $p $k }}
{{- end }}
{{- end }}
{{- end -}}
