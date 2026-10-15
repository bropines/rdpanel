{{/* vim: set filetype=mustache: */}}

{{- define "rustdesk-panel.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Full name. Kept to 50 chars so suffixed names (-rendezvous-tcp, -headless) stay <= 63.
*/}}
{{- define "rustdesk-panel.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "rustdesk-panel.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "rustdesk-panel.selectorLabels" -}}
app.kubernetes.io/name: {{ include "rustdesk-panel.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "rustdesk-panel.labels" -}}
helm.sh/chart: {{ include "rustdesk-panel.chart" . }}
{{ include "rustdesk-panel.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: rustdesk
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/* metadata.annotations block content (commonAnnotations merged with extra dict passed as .extra) */}}
{{- define "rustdesk-panel.annotations" -}}
{{- $a := merge (dict) (.extra | default dict) (.root.Values.commonAnnotations | default dict) -}}
{{- if $a -}}
annotations:
  {{- toYaml $a | nindent 2 }}
{{- end }}
{{- end -}}

{{- define "rustdesk-panel.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "rustdesk-panel.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "rustdesk-panel.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag | toString) -}}
{{- end -}}
{{- end -}}

{{/* Panel HTTP port, derived from panel.listen (":21114", "127.0.0.1:31114", "[::]:21114"). */}}
{{- define "rustdesk-panel.httpPort" -}}
{{- regexReplaceAll "^.*:" (toString .Values.panel.listen) "" -}}
{{- end -}}

{{- define "rustdesk-panel.secretName" -}}
{{- default (include "rustdesk-panel.fullname" .) .Values.secrets.existingSecret -}}
{{- end -}}

{{- define "rustdesk-panel.serverKeysSecretName" -}}
{{- default (printf "%s-server-keys" (include "rustdesk-panel.fullname" .)) .Values.serverKeys.existingSecret -}}
{{- end -}}

{{- define "rustdesk-panel.websocketServiceEnabled" -}}
{{- if or .Values.websocket.service.enabled .Values.websocket.ingress.enabled (and .Values.httpRoute.enabled .Values.httpRoute.websocketRules) -}}
true
{{- end -}}
{{- end -}}

{{- define "rustdesk-panel.dataClaimName" -}}
{{- if .Values.persistence.existingClaim -}}
{{- .Values.persistence.existingClaim -}}
{{- else -}}
{{- printf "data-%s-0" (include "rustdesk-panel.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/* Env vars shared by the server container and the migration job. */}}
{{- define "rustdesk-panel.env" -}}
- name: RUSTDESK_API_JWT_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "rustdesk-panel.secretName" . }}
      key: {{ .Values.secrets.jwtKeyKey }}
{{- if .Values.secrets.setSecretKey }}
- name: RDPANEL_SECRET_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "rustdesk-panel.secretName" . }}
      key: {{ .Values.secrets.secretKeyKey }}
{{- end }}
{{- end -}}

{{/* Generated probe handler. */}}
{{- define "rustdesk-panel.probeHandler" -}}
{{- if eq .Values.probes.type "exec" }}
exec:
  command:
    - wget
    - -q
    - -O
    - /dev/null
    - -T
    - "3"
    - http://127.0.0.1:{{ include "rustdesk-panel.httpPort" . }}{{ .Values.probes.path }}
{{- else }}
httpGet:
  path: {{ .Values.probes.path }}
  port: http
{{- end }}
{{- end -}}

{{- define "rustdesk-panel.probe" -}}
{{- $p := .probe -}}
{{ include "rustdesk-panel.probeHandler" .root | trim }}
{{- range $k := list "initialDelaySeconds" "periodSeconds" "timeoutSeconds" "failureThreshold" "successThreshold" }}
{{- if hasKey $p $k }}
{{ $k }}: {{ index $p $k }}
{{- end }}
{{- end }}
{{- end -}}
