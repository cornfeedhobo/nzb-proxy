{{- define "nzb-proxy.fullname" -}}
{{- if eq .Release.Name .Chart.Name -}}
{{ .Chart.Name }}
{{- else -}}
{{ printf "%s-%s" .Chart.Name .Release.Name }}
{{- end -}}
{{- end -}}

{{- define "nzb-proxy.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name | quote }}
app.kubernetes.io/instance: {{ .Release.Name | quote }}
{{- end -}}

{{- define "nzb-proxy.labels" -}}
{{ include "nzb-proxy.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
{{- end -}}

{{- define "nzb-proxy.image" -}}
{{- $repo := required "image.repository is required" .Values.image.repository -}}
{{- if contains "@" $repo -}}
{{ $repo }}
{{- else -}}
{{ printf "%s:%s" $repo (default .Chart.AppVersion .Values.image.tag) }}
{{- end -}}
{{- end -}}
