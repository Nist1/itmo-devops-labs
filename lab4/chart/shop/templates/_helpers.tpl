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

{{/*
Разнесение реплик по нодам. Механизм выбирается values.placement.mechanism:
topologySpread или antiAffinity. Вызов: include "shop.placement" (dict "root" . "component" "api")
В комментариях не должно быть двойных фигурных скобок: Helm исполняет их и там.
*/}}
{{- define "shop.placement" -}}
{{- $p := .root.Values.placement -}}
{{- if eq $p.mechanism "topologySpread" }}
topologySpreadConstraints:
  - maxSkew: {{ $p.maxSkew }}
    topologyKey: {{ $p.topologyKey }}
    whenUnsatisfiable: {{ $p.whenUnsatisfiable }}
    labelSelector:
      matchLabels:
        {{- include "shop.selectorLabels" . | nindent 8 }}
    matchLabelKeys:
      - pod-template-hash
{{- else if eq $p.mechanism "antiAffinity" }}
affinity:
  podAntiAffinity:
    {{- if eq $p.antiAffinityMode "required" }}
    requiredDuringSchedulingIgnoredDuringExecution:
      - topologyKey: {{ $p.topologyKey }}
        labelSelector:
          matchLabels:
            {{- include "shop.selectorLabels" . | nindent 12 }}
    {{- else }}
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          topologyKey: {{ $p.topologyKey }}
          labelSelector:
            matchLabels:
              {{- include "shop.selectorLabels" . | nindent 14 }}
    {{- end }}
{{- else }}
{{- fail (printf "placement.mechanism: ожидается topologySpread или antiAffinity, получено %q" $p.mechanism) }}
{{- end }}
{{- end }}
