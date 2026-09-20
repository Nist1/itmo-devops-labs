#!/usr/bin/env bash
# mydocker.sh — свой "docker run".
# Запускает api изолированным: свои namespaces (pid/mount/uts/ipc/net/user),
# лимиты cgroup (memory/cpu/pids), с урезанными capabilities и seccomp.

set -euo pipefail

LAB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
API_DIR="$LAB_DIR/api"
API_BIN="$API_DIR/api"
WRAP_BIN="$API_DIR/seccomp-wrap"
CGROUP="/sys/fs/cgroup/mydocker"

if [ ! -x "$API_BIN" ]; then
    echo "не найден бинарник api по пути $API_BIN" >&2
    exit 1
fi
if [ ! -x "$WRAP_BIN" ]; then
    echo "не найден seccomp-wrap по пути $WRAP_BIN" >&2
    exit 1
fi

echo "----- Настраиваем cgroup -----"
sudo mkdir -p "$CGROUP"
echo 200M            | sudo tee "$CGROUP/memory.max" >/dev/null
echo "50000 100000"  | sudo tee "$CGROUP/cpu.max"     >/dev/null
echo 50              | sudo tee "$CGROUP/pids.max"    >/dev/null
echo "----- memory.max=200M cpu.max=50000/100000(50%) pids.max=50 -----"

# Кладём ТЕКУЩИЙ shell в cgroup ДО запуска api
# дочерние процессы наследуют cgroup с момента появления.
echo $$ | sudo tee "$CGROUP/cgroup.procs" >/dev/null
echo "----- текущий shell помещён в cgroup mydocker -----"

echo "----- Запускаем api в своих namespaces и с урезанными правами -----"
unshare --pid --mount --uts --ipc --net --user --map-root-user --fork --mount-proc \
    chroot / /bin/sh -c "
        hostname mydocker
        ip link set lo up
        exec setpriv --bounding-set=-all --inh-caps=-all '$WRAP_BIN' '$API_BIN'" &

MYDOCKER_PID=$!
echo "----- Api запущен, shell-обёртка unshare — PID $MYDOCKER_PID -----"

wait "$MYDOCKER_PID"
