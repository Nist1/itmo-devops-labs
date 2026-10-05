{{/*
Префикс имён ресурсов. Релиз shop + чарт shop → "shop" (без дубля "shop-shop").
*/}}
{{- define "shop.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end }}

{{/*
Метки для селекторов (Deployment.spec.selector и Service.spec.selector).
Неизменяемы после создания, поэтому версии образа здесь нет.
Вызов: include "shop.selectorLabels" (dict "root" . "component" "api")
*/}}
{{- define "shop.selectorLabels" -}}
app.kubernetes.io/name: {{ .component }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
{{- end }}

{{/*
Полный набор меток. part-of требует политика require-ownership-labels.
Вызов: include "shop.labels" (dict "root" . "component" "api" "tag" .Values.api.tag)
*/}}
{{- define "shop.labels" -}}
{{ include "shop.selectorLabels" . }}
app.kubernetes.io/part-of: {{ .root.Values.partOf }}
app.kubernetes.io/version: {{ .tag | quote }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .root.Chart.Name .root.Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}
