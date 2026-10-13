#!/usr/bin/env bash
# break.sh — вносит одну случайную скрытую поломку связности через values (Часть 6).
# Какая выбрана — не печатается; ответ закодирован в .break-answer (base64).
# Починка после диагностики: helm upgrade shop ./chart/shop -n shop --reset-values --wait
set -eu
cd "$(dirname "$0")"

CHART=./chart/shop
NS=shop

VARIANTS=(
  "A|неверные лейблы в allow-правиле: селектор БД cnpg.io/cluster=shop-database|networkPolicy.database.podLabels.cnpg\.io/cluster=shop-database"
  "B|порт в правиле не совпадает с портом сервиса: БД разрешена на 5433 вместо 5432|networkPolicy.database.port=5433"
  "C|нет разрешения на DNS для клиента: dns.clients без loadgen|networkPolicy.dns.clients={api,worker}"
  "D|неверный порт DNS: 5353 вместо 53|networkPolicy.dns.port=5353"
)

pick=${VARIANTS[$((RANDOM % ${#VARIANTS[@]}))]}
IFS='|' read -r id desc set <<<"$pick"

START=$(date -u +%FT%TZ)

helm upgrade shop "$CHART" -n "$NS" --reset-values --set "$set" --wait >/dev/null

# Рестарт при ЛЮБОМ варианте: пулы соединений к БД переоткрываются, симптом проявляется сразу,
# и по факту рестарта вариант не угадать
kubectl -n "$NS" rollout restart deploy/shop-api deploy/shop-worker >/dev/null
kubectl -n "$NS" rollout status deploy/shop-api --timeout=180s >/dev/null
kubectl -n "$NS" rollout status deploy/shop-worker --timeout=180s >/dev/null

printf '%s|%s|%s\n' "$id" "$desc" "$START" | base64 > .break-answer
echo "$START поломка внесена" | tee -a break.log
echo "Ищи по трафику. Ответ после диагностики: base64 -d .break-answer"
