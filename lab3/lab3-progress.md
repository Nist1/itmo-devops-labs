# Лаба 3 — журнал работы (продолжение)

Прежний журнал и сгенерированные файлы лежали в scratchpad сессии, он больше недоступен. Дальше журнал и новые файлы — в этой директории сессии.
Копии уже переданных на ВМ файлов есть на самой ВМ: `~/devops-labs/lab3/`.

Среда: ВМ `itmo-devops-labs` (user `nist`), minikube (3 CPU / 6 GB), k8s v1.37.0, containerd. Локально нет Go и мониторинга; кластер смотрим через Freelens (не забывать фильтр namespace = All).

## Состояние на 2026-10-05

### Этап 0 — готов
- Образы `registry.local/shop/api:0.1.0` и `registry.local/shop/worker:0.1.0` собраны через `minikube image build` (изнутри каталога, после `go mod tidy`; без go.sum сборка падает молча).
- Код на ВМ: `~/devops-labs/lab3/shop/{api,worker}`.
- api: `/health` (не зависит от БД; `HEALTH_FAIL=true` → 500), `/version`, `POST /order`, `GET /orders`, `/metrics`; DATABASE_URL из env; graceful shutdown по SIGTERM (20 с).
- worker: опрашивает БД (`FOR UPDATE SKIP LOCKED`), `/health`, `/metrics`.

### Этап 1 — готов (Kyverno v1.19.1, chart 3.9.1, ns kyverno)
Пять `ValidatingPolicy` (policies.kyverno.io/v1), все `Deny`, autogen на контроллеры, системные ns исключены
(kube-system, kube-public, kube-node-lease, kyverno, monitoring, local-path-storage, cnpg-system):
1. require-resource-limits — requests.cpu, requests.memory, limits.memory (лимит CPU не требуем);
2. restrict-image-registries — `registry.local/shop/`, `ghcr.io/cloudnative-pg/`;
3. require-ownership-labels — `app.kubernetes.io/name`, `app.kubernetes.io/part-of`;
4. disallow-privileged-containers — privileged и allowPrivilegeEscalation: true;
5. disallow-latest-tag — явный тег или digest, не latest.
Нарушители `policies/violations/`: 00-compliant (проходит), 01, 02, 03, 03b (Deployment, autogen), 04, 05, 05b (без тега) — все отклоняются как ожидалось (подтверждено студентом).
Файлы на ВМ: `~/devops-labs/lab3/policies/`.

Истории по ходу: политика 1 однажды пропала из кластера (вероятно удалена руками; признак — `kyverno-resource-validating-webhook-cfg` с 0 webhooks), применена заново. Ошибка strict decoding была из-за опечатки `matchExpression`.

### Состояние кластера
- helm: kps, loki, alloy, jaeger (оставлен по решению студента), alert-webhook, karma (все monitoring), kyverno. Старый `api` из Лабы 2 удалён.
- В ns monitoring мог остаться тестовый под `t` без лимитов — удалить (`kubectl -n monitoring delete pod t`).
- `Allocated resources` ноды ещё не присылал.

## Дальше
- Скриншоты Части 1: `kubectl get vpol`, webhook-конфиг, прогон `violations/`, `status.autogen`, PolicyReport, Freelens.
- Этап 2: Helm-чарт `chart/shop` (api 3 реплики, worker 2, пробы на /health, метки part-of, лимиты, IfNotPresent, securityContext, DATABASE_URL с `optional: true`), установка под политиками, reconciliation, rolling update, поломанный релиз + rollback.
- Этап 3 (CNPG), этап 4 (падение control plane), этап 5 (ServiceMonitor/PrometheusRule на kps), README и скриншоты.

## Договорённости
Без вопросов-гейтов; файлы целиком; по одному этапу за раз; пары «политика + нарушитель»; Go; не писать в репозиторий; ход работы — в этот файл.

## Этап 2 — чарт (2026-10-05)
- Решения студента: чарт shop отдельный от lab2/helm/api (конфликта нет: релиз shop в ns shop, старый api удалён); сразу «правильная» версия без наивной.
- Мониторинг Части 5: стек kps используем как есть, шаблоны ServiceMonitor/PrometheusRule/dashboard копируем из lab2 и адаптируем (subchart не подходит: тянет Deployment api).
  В lab2 dashboard.yaml путь `dashboards/api-red.json` не совпадает с реальным `dashboard/api-RED-dashboard.json`.
- Файлы: `chart/shop/{Chart.yaml,values.yaml,templates/_helpers.tpl,api-deployment.yaml,api-service.yaml,worker-deployment.yaml,worker-service.yaml}` в директории сессии.
- Особенности: релиз shop + чарт shop → fullname `shop` (Deployments shop-api, shop-worker); part-of на шаблоне пода; maxUnavailable 0, maxSurge 1;
  preStop `sleep` (distroless без shell; нативное действие kubelet); DATABASE_URL из секрета `shop-db-app` (ключ uri) с `optional: true` — после Части 3 нужен rollout restart.
- Ещё не проверено на кластере: helm lint, helm template | kubectl apply --dry-run=server, helm install.
- Чарт на ВМ: `~/devops-labs/lab3/chart/shop`. Затык: каталог шаблонов на ВМ назывался `tepmlates` → `helm template` молча выдал пусто, kubectl: "no objects passed to apply" (та же ловушка «чарт без шаблонов», что в Лабе 2). Исправлено `mv tepmlates templates`.
- dry-run=server: 4 объекта прошли admission/Kyverno. `helm install shop chart/shop -n shop` — REVISION 1, deployed с первого раза, без отказов политик.
  Поды: shop-api x3, shop-worker x2 — Running 1/1; EndpointSlice shop-api = 3 IP, shop-worker = 2 IP; Service-селекторы name+instance.
- Порт-форвард 8080 запущен; результаты curl (/health, /version, /orders=503) ещё не получены.
- Идея для README/скриншота: показать, что чарт реально под ограждением — `helm install ... --dry-run=server --set image.registry=docker.io/library` (или `--set api.resources=null`) должен упасть на политике.
- Дальше (Этап 2.2): reconciliation — удалить под api, поменять replicas + helm upgrade; затем rolling update (цикл curl) и сломанный релиз HEALTH_FAIL + rollback.
- Студент пропустил вывод reconciliation (delete pod / replicas) и просит перейти к rolling update. (Для README потребуются вывод `get pods -w` при удалении пода и `get rs` / `helm history` после replicas 5.)
### Этап 2.3 — rolling update
- Нужен образ 0.2.0: `cd ~/devops-labs/lab3/shop/api && minikube image build -t registry.local/shop/api:0.2.0 .` (тот же код, слои из кэша; отличается только тег → APP_VERSION в /version).
- Нагрузка: НЕ через `kubectl port-forward svc/...` (форвард прибит к одному поду и умрёт вместе с ним). Бьём из ноды по ClusterIP:
  `IP=$(kubectl -n shop get svc shop-api -o jsonpath='{.spec.clusterIP}'); minikube ssh -- "for i in \$(seq 1 1200); do curl -s -o /dev/null -m 2 -w '%{http_code}\n' http://$IP:8080/health; sleep 0.1; done | sort | uniq -c"`.
  Лоадер-под в кластере неудобен: политики Kyverno применяются и к нему (curl-образ не из доверенного реестра) — только в monitoring (исключён).
- Ожидание: `1200 200`; поды меняются по одному (maxSurge 1, maxUnavailable 0); два ReplicaSet (старый scaled to 0 для rollback).

### Reconciliation — результат (скриншоты Screenshots\6.png, 7.png)
- 6.png: curl: /health=ok, /version={"pod":"shop-api-7dd8c49c7f-4s5ft","version":"0.1.0"}, /orders=503; `kubectl -n shop delete pod shop-api-7dd8c49c7f-mdng7`;
  Lens: mdng7 Terminating (5m28s), новый shop-api-7dd8c49c7f-2xlxr Running уже в 3s → замена создаётся не дожидаясь завершения старого.
- 7.png: `nano values.yaml` (api.replicas=4); первый `helm upgrade` без аргументов → «requires 2 arguments»; затем `helm upgrade shop chart/shop -n shop` → REVISION 2;
  Lens: 4 api-пода, новый w2mlg 4s (ещё не Ready), хеш RS тот же 7dd8c49c7f → новый ReplicaSet при смене replicas не создаётся.
- Создан черновик отчёта `README.md` в директории сессии (Части 0, 1, 2 до reconciliation; остальное TODO). Скриншоты 6.png и 7.png лежат в C:\Users\nstep\OneDrive\Изображения\Screenshots\ — в отчёте названы screenshots/6.png и 7.png (переименовать/скопировать при сдаче).
- Дальше: rolling update (образ 0.2.0, нагрузка из ноды), сломанный релиз + rollback.

### Rolling update — результат (2026-10-05)
- Первая попытка `helm upgrade` провалилась: запускали из chart/shop → `Error: repo chart not found` (относительный путь chart/shop; Helm трактует как repo/chart). Урок: helm upgrade запускать из ~/devops-labs/lab3.
- В 14:02:53 api 0.1.0→0.2.0 (REVISION 4): новый RS shop-api-68dc444b5d 3/3/3, старый 7dd8c49c7f scaled to 0 (остаётся для rollback). rollout status: 0→1→2→3 of 3 new replicas updated → "1 old replicas are pending termination" → successfully rolled out. В Lens: 3 новых пода (4–6 с) + 3 старых Terminating (кратковременно перекрываются — preStop sleep 5 с).
- Гистограмма с первой попыткой не вывелась: `sort | uniq -c` печатает только по завершении цикла (1200×0.1 с ≥ 2 мин), а смотрели раньше.
- Повторный тест: цикл 300 запросов по ClusterIP из ноды (`minikube ssh` → curl ClusterIP:8080/health) → результат `300 200` (ни одного 000/5xx).
  ОГОВОРКА: неподтверждено, шёл ли `helm upgrade --set api.tag=0.1.0` одновременно с циклом — уточнить у студента.
- Нужно для отчёта: вывод `get pods -w` (или describe deploy: события scale up/down нового/старого RS) — показать последовательность по одному.
- Студент подтвердил: нагрузка (300×200) шла параллельно с `helm upgrade` → потерь запросов нет. Rolling update ПОДТВЕРЖДЁН.
- Скриншот `describe deploy shop-api | tail -20` (events): чередование «Scaled up 68dc444b5d 0→1 / down 7dd8c49c7f 3→2 / up 1→2 / down 2→1 / up 2→3 / down 1→0» (15m назад, обновление 0.1.0→0.2.0),
  затем (7m55s) обратное: up 7dd8c49c7f 0→1, down 68dc 3→2, up 1→2, … down 68dc 1→0. Всегда up раньше down → maxSurge 1 / maxUnavailable 0, по одному поду.
  Заметка: при обратном обновлении Deployment ПЕРЕИСПОЛЬЗОВАЛ старый RS 7dd8c49c7f (шаблон идентичен → тот же pod-template-hash), новый RS не создавался — тот же механизм использует helm rollback.
- Дальше: сломанный релиз (HEALTH_FAIL=true) + rollback.

### Сломанный релиз (HEALTH_FAIL=true) — наблюдения (скриншот 10)
- `helm upgrade shop chart/shop -n shop --set api.healthFail=true`; `rollout status --timeout=60s` → "1 out of 3 new replicas have been updated..." → "error: timed out waiting for the condition".
- RS: новый shop-api-58dd9d576b desired 1 / current 1 / ready 0; старый 7dd8c49c7f 3/3/3 (живой); 68dc444b5d 0/0/0. Под shop-api-58dd9d576b-w98cj 0/1 Running, restarts 1 (15s ago), Lens: "Readiness probe failed: HTTP probe failed with statuscode: 500".
  Три старых пода 1/1 → service продолжает отвечать (maxUnavailable 0 + readiness не пускает новый под в EndpointSlice).
- Рестарты нового пода — от liveness (тот же /health, ~40 с: initialDelay 10 + 3×10) → CrashLoop; урок: liveness на том же эндпоинте, что и readiness, рестартит под при любой проблеме, читаемой /health.
- Ошибка студента: `describe pod <новый под> | tail` → `-bash: syntax error near unexpected token |` (плейсхолдер в угловых скобках — это редирект). Нужно имя пода: shop-api-58dd9d576b-w98cj.
- Нюанс: `--set` не сохраняется между upgrade (без --reuse-values берётся values.yaml). Если в values.yaml api.tag остался 0.2.0, то «сломанная» ревизия заодно сменила тег 0.1.0→0.2.0 (RS 58dd9d576b ≠ ни 7dd8c49c7f, ни 68dc444b5d). Проверить `helm history` (ревизии 5 и 6) и `kubectl get deploy -o yaml | grep image`.
- Гистограмма нагрузки (ожидаем 600×200) ещё не прислана.
- Дальше: `helm rollback` на последнюю рабочую ревизию (вероятно 5), показать новую ревизию «Rollback to N».
- Нагрузка во время сломанного релиза: `600 200` (600 запросов по ClusterIP из ноды, ни одного 000/5xx) → сервис отвечал всё время зависшего rollout (подразумевается параллельный запуск с upgrade; студент не оспаривал).
- Ещё не получено: Events нового пода (describe pod shop-api-58dd9d576b-w98cj), полный `helm history`, результат `helm rollback`.

### Rollback — результат (скриншот 12) — ЭТАП 2 ЗАКРЫТ
- Перед откатом студент правил values.yaml (nano) — rollback берёт значения из сохранённой ревизии, файл не трогает; следить, чтобы values.yaml отражал желаемое состояние (следующий upgrade берёт его).
- `helm rollback shop 5 -n shop` → ревизия 7 «Rollback to 5», deployed. Ревизия 6 (15:05:36, сломанная, HEALTH_FAIL=true) → superseded; ревизия 5 (14:10:03) — рабочая 0.1.0.
- RS: 58dd9d576b 0/0/0 (сломанный), 68dc444b5d 0/0/0, 7dd8c49c7f 3/3/3 (рабочий, переиспользован). 3 api-пода (60m) не перезапускались, 2 worker. Сломанный под w98cj в момент вывода `Completed, 5 restarts (67s ago)` → затем исчез.
- История: 1 install, 2 replicas=4, 3 replicas=3, 4 api 0.2.0, 5 api 0.1.0, 6 healthFail=true (сломанная), 7 rollback to 5.
- Выводы для README: (1) maxUnavailable 0 + readiness → сервис отвечал (600×200); (2) helm upgrade без --wait вернул deployed при зависшем rollout; (3) liveness на том же /health рестартил сломанный под (5 рестартов); (4) откат — новая ревизия 7, номера не возвращаются; (5) Deployment переиспользует RS с тем же hash.
- Нет: Events пода w98cj, вывод rollout status после отката (обрезан), скриншот гистограммы.

### Этап 3 — CloudNativePG (старт)
- Создан chart/shop/templates/postgres-cluster.yaml (Cluster shop-db, inheritedMetadata labels name=postgres + part-of, resources, bootstrap.initdb db/owner). imageName не задан (дефолт оператора из ghcr.io/cloudnative-pg/ с тегом).
- Нужно добавить в values.yaml секцию database: instances 1, storageSize 1Gi, name shop, owner shop, resources (requests 100m/256Mi, limits memory 512Mi).
- Оператор: helm repo cnpg → `cnpg/cloudnative-pg` в ns cnpg-system (ns исключён из политик).
- Предполагаемые трения с Kyverno: initContainer bootstrap-controller и initdb-Job — resources/labels/securityContext; проверять по отказам.
- Allocated resources ноды студент так и не присылал — запросить перед установкой оператора.
- Allocated resources ноды (до CNPG): cpu requests 1700m (42%) / limits 200m (5%); memory requests 1966Mi (24%) / limits 5980Mi (75%). Из процентов: ёмкость ноды ≈ 4 CPU и ≈ 8 GB (больше, чем 3 CPU/6 GB из Лабы 2). Запас по requests большой (~2.3 CPU, ~6 GB) → Jaeger оставляем, оператор + postgres (100m/256Mi) помещаются. Лимиты памяти 75% — овербукинг допустим, но реальное потребление не показано.

### Этап 3 — CNPG: установка Cluster (2026-10-05, ~15:33)
- Оператор cnpg установлен (cnpg-system). Шаблон postgres-cluster.yaml + database.* в values.yaml добавлены; `helm upgrade shop` → REVISION 8 (создан Cluster).
- Через ~1 с оператор создал секреты: shop-db-app (basic-auth, 11 ключей, включая uri), shop-db-ca, shop-db-replication, shop-db-server. Политики Kyverno на этом этапе ничего не отклонили (по выводу).
- Снимок был до появления подов БД (pods: api×3, worker×1 в списке — вывод обрезан). Студент сразу сделал `rollout restart deploy/shop-api deploy/shop-worker` (до готовности БД; api/worker ретраят подключение каждые 5 с/2 с, так что не страшно) и ещё раз `helm upgrade shop chart/shop -n shop` → REVISION 9 (лишний, безвредный: ручные аннотации restartedAt сохраняются при 3-way merge).
- Нет данных: состояние pod/shop-db-1, PVC, Services, cluster status; результат POST /order → worker.
- Скриншот 15: Lens, ns shop: 6 подов Running — shop-worker×2, shop-db-1 (Controlled By Cluster), shop-api×3. БД поднялась, rollout api завершён.
  `POST /order` (port-forward svc/shop-api 8080) → {"id":1,"item":"book","qty":2,"status":"new","created_at":"2026-10-05T15:36:43Z"} → цепочка api→postgres работает (схема создана api).
  Ошибка: команды `curl -X POST ... -d '{...}' sleep 3; curl -s localhost:8080/orders` склеились в одну строку (потерян перевод строки при вставке) → curl принял `sleep`/`3` за URL; GET /orders не отработал, студент нажал ^C.
  `kubectl logs deploy/shop-worker --tail=5` взял один из двух подов (j55nl): там только "worker http on :8080, poll every 2s" и "database ready", без "processed" — заказ мог обработать второй под (SKIP LOCKED). Нужно `logs -l app.kubernetes.io/name=worker --prefix` и повторный GET /orders (ждём status=processed).
- Дальше: удалить под shop-db-1 → оператор вернёт; данные должны сохраниться (PVC); `kubectl get cluster shop-db -o yaml` — spec/status.
