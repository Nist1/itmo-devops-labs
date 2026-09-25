#!/usr/bin/env bash
# Держит все port-forward лабы 2 живыми.
# Каждый форвард крутится в цикле: упал (под пересоздан, оборвался стрим) — перезапуск через 2 с.
# setsid + nohup — циклы не привязаны к SSH-сессии и переживают её обрыв.
# Повторный запуск скрипта сначала гасит старые циклы и форварды.

pkill -f 'pf-keep' 2>/dev/null
pkill -f 'kubectl.*port-forward' 2>/dev/null
sleep 1

keep() {  # keep <namespace> <service> <local:remote>
  setsid nohup bash -c \
    "while :; do kubectl -n $1 port-forward svc/$2 $3 >/dev/null 2>&1; sleep 2; done # pf-keep" \
    >/dev/null 2>&1 &
}

keep app        api                                    8080:8080
keep monitoring kps-kube-prometheus-stack-prometheus   9090:9090
keep monitoring kps-grafana                            3000:80
keep monitoring jaeger                                 16686:16686
keep monitoring kps-kube-prometheus-stack-alertmanager 9093:9093
keep monitoring karma				       8081:80

sleep 4
echo "Слушают:"
ss -ltn | grep -E ':(8080|9090|3000|16686|9093|8081) ' || echo "ни один форвард не поднялся"
echo
echo "Остановить всё: pkill -f pf-keep; pkill -f 'kubectl.*port-forward'"
