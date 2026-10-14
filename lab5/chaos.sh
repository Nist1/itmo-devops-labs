#!/usr/bin/env bash
set -u
NS=shop
INTERVAL=${1:-10}
DURATION=${2:-0}
LOG=chaos.log

KILLED_API=0
KILLED_WORKER=0
START_TS=$(date -u +%FT%TZ)

summary() {
  echo
  echo "итого с $START_TS по $(date -u +%FT%TZ): убито $((KILLED_API + KILLED_WORKER)) (api $KILLED_API, worker $KILLED_WORKER)" | tee -a "$LOG"
}
trap summary EXIT
trap 'exit 130' INT TERM

candidates() {
  kubectl -n "$NS" get pods -l 'app.kubernetes.io/name in (api,worker)' -o go-template='
{{- range .items -}}
  {{- if not .metadata.deletionTimestamp -}}
    {{- $pod := . -}}
    {{- range .status.conditions -}}
      {{- if and (eq .type "Ready") (eq .status "True") -}}
        {{ $pod.metadata.name }} {{ $pod.status.podIP }} {{ $pod.spec.nodeName }}{{ "\n" }}
      {{- end -}}
    {{- end -}}
  {{- end -}}
{{- end -}}'
}

END=$((SECONDS + DURATION))
while [ "$DURATION" -eq 0 ] || [ "$SECONDS" -lt "$END" ]; do
  line=$(candidates | shuf -n1)
  [ -z "$line" ] && { echo "нет Ready-кандидатов, пропуск"; sleep "$INTERVAL"; continue; }
  read -r name ip node <<< "$line"
  kubectl -n "$NS" delete pod "$name" --wait=false >/dev/null
  printf '%s KILL %-32s ip=%-14s node=%s\n' "$(date -u +%FT%TZ)" "$name" "$ip" "$node" | tee -a "$LOG"
  case "$name" in
    shop-api-*)    KILLED_API=$((KILLED_API + 1)) ;;
    shop-worker-*) KILLED_WORKER=$((KILLED_WORKER + 1)) ;;
  esac
  sleep "$INTERVAL"
done

