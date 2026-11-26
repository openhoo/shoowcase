{{/* Helm release names are at most 53 characters. Only non-Service-compatible
     names need an alias; use 54 characters then so it cannot shadow a literal release name. */}}
{{- define "shoowcase.fullname" -}}
{{- if regexMatch "^[a-z][a-z0-9-]*$" .Release.Name -}}
{{- .Release.Name -}}
{{- else -}}
{{- printf "s-%s" (sha256sum .Release.Name | trunc 52) -}}
{{- end -}}
{{- end -}}

{{- define "shoowcase.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name | quote }}
app.kubernetes.io/instance: {{ .Release.Name | quote }}
{{- end -}}

{{- define "shoowcase.labels" -}}
{{ include "shoowcase.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" | quote }}
{{- end -}}

{{- define "shoowcase.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "shoowcase.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "shoowcase.httpGet" -}}
httpGet:
  path: {{ .path }}
  port: http
  httpHeaders:
    - name: Host
      value: {{ .host | quote }}
{{- end -}}

{{- define "shoowcase.secretEnv" -}}
- name: {{ .env }}
  valueFrom:
    secretKeyRef:
      name: {{ .secret | quote }}
      key: {{ .key | quote }}
{{- end -}}

{{/* The app's public suffix list is authoritative, including private and multi-label suffixes. */}}
{{- define "shoowcase.validate" -}}
{{- $cfg := .Values.config -}}
{{- if or (eq $cfg.controlHost $cfg.sitesDomain) (hasSuffix (printf ".%s" $cfg.sitesDomain) $cfg.controlHost) (hasSuffix (printf ".%s" $cfg.controlHost) $cfg.sitesDomain) -}}
{{- fail "config.controlHost and config.sitesDomain must use separate registrable domains" -}}
{{- end -}}
{{- if eq $cfg.oidc.clientId $cfg.oidc.audience -}}
{{- fail "config.oidc.audience must differ from the browser clientId" -}}
{{- end -}}
{{- if gt (int .Values.autoscaling.minReplicas) (int .Values.autoscaling.maxReplicas) -}}
{{- fail "autoscaling.maxReplicas must be at least autoscaling.minReplicas" -}}
{{- end -}}
{{- $keys := values .Values.existingSecret.keys -}}
{{- if ne (len $keys) (len (uniq $keys)) -}}
{{- fail "existingSecret.keys must reference three distinct keys" -}}
{{- end -}}
{{- end -}}
