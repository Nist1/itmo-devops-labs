# Лаба 3 — журнал работы

Среда: ВМ `itmo-devops-labs` (user `nist`), `~/devops-labs/lab3`, minikube v1.39.0, k8s v1.37.0, runtime containerd 2.3.4, IP ноды 192.168.49.2.
Локально (Windows) нет ни Go, ни мониторинга — всё собирается и запускается на ВМ; кластер смотрим через Freelens.

## План этапов
0. Сервисы api/worker (Go) + Dockerfile, сборка образов, загрузка в minikube
1. Движок политик + 5 правил + 5 нарушающих манифестов
2. Helm-чарт api/worker, чистая установка под политиками, reconciliation
3. Rolling update без потерь + сломанный релиз (HEALTH_FAIL) + rollback
4. CloudNativePG, Cluster в чарте, удаление пода БД, spec/status
5. Падение control plane
6. ServiceMonitor/PrometheusRule на стеке из Лабы 2, 3 алерта, firing
7. README + скриншоты

## Ход работы

### 2026-10-03 — подготовка
- `minikube start` (существующий профиль), `minikube ip` → 192.168.49.2.
- Kubeconfig для Freelens: `kubectl config view --minify --flatten --context=minikube > ~/minikube-lens.yaml` (flatten — встраивает сертификаты прямо в файл, иначе Freelens не найдёт пути к ~/.minikube/*.crt).
- Freelens настроен.

### Этап 0 — сервисы
- Сгенерированы `api/` и `worker/` (main.go, go.mod, Dockerfile).
- Решения в коде, важные для следующих частей:
  - `/health` не зависит от БД → чарт из Части 2 ставится до появления postgres, и в Части 4 /health отвечает независимо от control plane.
  - pgxpool коннектится лениво, схема создаётся фоновым ретраем → поды не падают в CrashLoop, пока БД нет.
  - `HEALTH_FAIL=true` → /health отдаёт 500 (Часть 2, сломанный релиз).
  - Graceful shutdown по SIGTERM (Shutdown с таймаутом 20s) — нужен для rolling update без ошибок.
  - `/version` возвращает APP_VERSION + имя пода — видно, какая версия ответила во время rolling update.
  - `/metrics` (client_golang) уже сейчас → в Части 5 не пересобирать.
  - DATABASE_URL — под ключ `uri` секрета `<cluster>-app`, который создаёт CloudNativePG.
  - Образ distroless:nonroot, UID 65532 → пройдёт политики non-root.
- Имя образа: `registry.local/shop/api:0.1.0` — хост с точкой в префиксе, чтобы правило «доверенный реестр» сравнивало явный префикс (образы без хоста Kyverno нормализует к docker.io/...).
- Статус: файлы переданы, жду сборку на ВМ.

### Контекст из Лабы 2 (D:\Projects\itmo-devops-labs\lab2)
- minikube: `--cpus=3 --memory=6g --disk-size=40g` — ресурсов мало, лишнее из прошлой лабы надо убрать.
- В кластере: релиз `api` (ns `app`, образ `api:0.1.0`, ServiceMonitor + PrometheusRule ApiHighErrorRate/ApiHighLatencyP95/ApiDown + ConfigMap дашборда);
  в ns `monitoring`: `kps` (kube-prometheus-stack), `loki`, `alloy`, `jaeger`, `alert-webhook`, `karma`.
- kps: `serviceMonitorSelectorNilUsesHelmValues/ruleSelectorNilUsesHelmValues: false` → ServiceMonitor/PrometheusRule из чарта shop подхватятся из любого ns без метки release.
- Grafana sidecar подхватывает ConfigMap с `grafana_dashboard: "1"`; Alertmanager шлёт `severity=critical` в webhook.
- Уроки: `honorLabels: true` (конфликт метки endpoint), `helm template | kubectl apply --dry-run=server` вместо одного `helm lint`, `absent(up==1)` для ApiDown.
- Образы в Лабе 2 собирались через `minikube image build` — делаем так же (Dockerfile переведён на golang:1.27, как в lab2).

### Решение: убрать старый api (jaeger — по ресурсам)
- `helm uninstall api -n app` (+ ns app) — экономия ресурсов, нет дублирующих алертов, Kyverno не будет блокировать пересоздание старых подов.
- Стек kps/loki/alloy/alert-webhook/karma оставляем — Часть 5 требует использовать его.
- Jaeger: удалять НЕ обязательно. Аргумент только ресурсы — новые api/worker без OTel, трейсов не шлют, Jaeger будет простаивать.
  Против удаления: он часть стека Лабы 2, и на него ссылаются datasource Jaeger + derivedFields у Loki в values kps (кнопка Open in Jaeger сломается).
  Решаем по `Allocated resources` ноды; если памяти хватает — оставляем.
- Итог: Jaeger оставляем (решение студента). Удаляем только старый релиз `api` + ns `app`.

### Сборка образов — затык
- `minikube image build -t registry.local/shop/{api,worker}:0.1.0 {api,worker}/` завершилась молча, но в `minikube image ls` образов нет.
  Старый `docker.io/library/api:0.1.0` из Лабы 2 на месте (минор: можно `minikube image rm`).
- Гипотезы: не выполнен `go mod tidy` → нет go.sum → `COPY go.mod go.sum` падает; minikube на containerd-рантайме глотает ошибку buildkit без -v.
- Диагностика: exit code, `ls api/`, сборка с `--alsologtostderr -v=1`, сборка изнутри каталога как в lab2.
- Решение: после `go mod tidy` (появился go.sum) сборка изнутри каталога прошла: `cd shop/api && minikube image build -t registry.local/shop/api:0.1.0 .`
  — все 6 шагов build-этапа + `naming to registry.local/shop/api:0.1.0 done`. Вывод: первая «тихая» сборка падала на отсутствии go.sum, minikube глотал ошибку.
- Флаги `--alsologtostderr -v=1` ушли на отдельную строку при вставке → `command not found`, на сборку не повлияло.
- Каталог на ВМ: `~/devops-labs/lab3/shop/{api,worker}`. На ВМ лежит старая версия Dockerfile с `golang:1.27-alpine` — работает, менять не обязательно.
- worker собран, оба образа `registry.local/shop/{api,worker}:0.1.0` в ноде. Этап 0 закрыт.
  (helm list -A / Allocated resources так и не присланы — запросить при установке Kyverno.)

### Этап 1 — политики
- Рекомендован Kyverno: правила = обычные манифесты, autogen правил с Pod на контроллеры (Deployment/StatefulSet/Job),
  PolicyReport для фоновой проверки; Gatekeeper = ConstraintTemplate(Rego) + Constraint — дороже на входе.
- Файл: `policies/kyverno-values.yaml` — 1 реплика admission, урезанные ресурсы, reports on, cleanup off.
- Ключевые грабли, заложенные заранее:
  - исключить системные ns (kube-system, kyverno, monitoring, local-path-storage, cnpg-system) — иначе стек Лабы 2 при пересоздании подов упрётся в политики;
  - postgres через CNPG будет в ns shop → в доверенные реестры нужен ghcr.io/cloudnative-pg/*, лимиты/метки поды CNPG получат из Cluster.spec.resources / inheritedMetadata;
  - не делать правило «обязательные пробы» на все поды — initdb-Job CNPG проб не имеет.
- Статус: дал установку Kyverno + каркас ClusterPolicy, студент пишет 5 правил сам.
- Kyverno установлен: chart 3.9.1, Kyverno v1.19.1, ns kyverno, 1 реплика admission (warning про HA — ожидаемо).
  NOTES: kyverno.io ClusterPolicy/Policy — deprecated → переходим на `policies.kyverno.io/v1 ValidatingPolicy` (CEL, та же модель, что у нативного ValidatingAdmissionPolicy).
  В выводе api-resources нет clusterpolicies (возможно, обрезано при вставке) — неважно, пишем на vpol.
- Студент хотел сразу задеплоить api/worker — объяснено: они заходят чартом на Этапе 2, после политик (требование «ограждения заведены раньше чарта», и проверка чарта политикой — смысл Части 2).
- Структура на ВМ (~/devops-labs/lab3), её же зеркалим в D:\Projects\itmo-devops-labs\lab3:
  shop/{api,worker} — код; install/kyverno-values.yaml — values инфраструктурных чартов;
  policies/NN-*.yaml — только ValidatingPolicy (чтобы `kubectl apply -f policies/` не споткнулся о values);
  policies/violations/NN-*.yaml — нарушители; chart/shop — Helm-чарт (Этап 2); README.md, screenshots/.
  В scratchpad kyverno-values.yaml перенесён в install/.
- Согласованы имена и описания 5 правил → `policies/RULES.md` (заготовка для раздела README Части 1):
  1 require-resource-limits (requests cpu/mem + limits.memory, лимит CPU сознательно не требуем),
  2 restrict-image-registries (registry.local/shop/, ghcr.io/cloudnative-pg/),
  3 require-ownership-labels (app.kubernetes.io/name + part-of),
  4 disallow-privileged-containers (privileged + allowPrivilegeEscalation),
  5 disallow-latest-tag (своё).
  CEL-выражения пишет студент.
- Студент предложил режимы: limits=Warn, registry=Deny, labels=Warn, privileged=Deny, latest=Deny.
  Разобрано: Warn не отклоняет → нарушители 01/03 пройдут (задание требует «отклоняются»), и в Части 2 helm install не упадёт на метках.
  Рекомендация: в лабе все Deny; в README — прод-раскатка Audit → Warn → Deny (Warn и Deny в одной политике несовместимы),
  можно показать фазу [Warn, Audit] + PolicyReport перед переключением на Deny. Ждём решение студента.
- Решение: все 5 правил в Deny; идея прод-раскатки Audit→Warn→Deny — абзацем в README. Студент пишет 01-require-limits.yaml.
- Первая версия 01: по сути каркас с плейсхолдерами. Замечания: имя не по согласованному (require-resource-limits);
  description обрезан (`должен>` — артефакт отображения nano или реальный обрыв, проверить cat);
  `namespaceSelector: {}` = все ns, включая системные; нет autogen; expression — плейсхолдер; у message нет закрывающей кавычки → YAML не распарсится.
  Дал: namespaceSelector NotIn по kubernetes.io/metadata.name, variable со склейкой containers+initContainers, начало .all() — остальное дописывает студент.
- 01 v2: CEL-выражение и variables верные. Осталось: namespaceSelector без `matchExpressions:` (LabelSelector — объект, список сразу под ним не пройдёт схему);
  autogen не добавлен; description/message не совпадают с проверкой (пишут «лимиты cpu», а проверяются requests cpu/mem + limits.memory); имя всё ещё require-limits.
- 01 итоговый собран по просьбе студента из его версии (+matchExpressions, autogen.podControllers.controllers, имя, description/message) → policies/01-require-limits.yaml. Ждём apply и dry-run.
- apply 01: `strict decoding error: unknown field ...namespaceSelector.matchExpression` — опечатка (без s) в версии на ВМ (правил руками, не scp). Strict field validation apiserver (kubectl --validate=strict по умолчанию) ловит неизвестные поля до Kyverno.
- 01 применена как `require-resources-limits` (имя студента). Проверки:
  - Pod в shop → отказ от webhook `vpol.validate.kyverno.svc-fail` (суффикс -fail = failurePolicy Fail);
  - Deployment в shop → отказ на самом Deployment → autogen работает;
  - monitoring: из-за переноса строки `--dry-run=server` отвалился → под `t` РЕАЛЬНО создан в monitoring (исключение ns работает, но под без лимитов надо удалить: `kubectl -n monitoring delete pod t`).
  - message остался старый («лимиты по памяти») — поправить на все 3 поля.
  Совет: защитные флаги (--dry-run=server) ставить в начало команды — при обрыве строки хвост без флага не выполнится «вживую».
- Дальше политики 2–5: подсказки — список префиксов + exists/startsWith (2), `in object.metadata.labels` (3), optional-синтаксис `c.?securityContext.?privileged.orValue(false)` (4), разбор имени образа по '@' и последнему '/' (5).
- Обсудили отчёт по Части 1 и нарушителей. Принцип: базовый «чистый» под (00-compliant, должен проходить) + 5 файлов, каждый отличается от базы ОДНИМ полем → ровно одно нарушение.
  Грабли 04: privileged:true + allowPrivilegeEscalation:false отклоняет сама валидация Pod в apiserver (до validating webhook) — в 04 строку allowPrivilegeEscalation убрать.
  Доп. кейсы (опц.): initContainer без лимитов, образ без тега, Deployment-вариант для демонстрации autogen.
- Порядок: сначала 00-compliant + 01-no-limits (для уже готовой политики 1), дальше парами «политика N + нарушитель N» с проверкой после каждой пары; в конце общий прогон для скриншота.
- Сгенерирован policies/violations/01-no-limits.yaml (Pod violation-no-limits в shop, всё по базе, кроме resources). 00-compliant = он же + resources.

## >>> ТЕКУЩЕЕ СОСТОЯНИЕ (2026-10-03, пауза) <<<
Этап 0 — готов: образы registry.local/shop/{api,worker}:0.1.0 в ноде minikube; код на ВМ в ~/devops-labs/lab3/shop/.
Этап 1 (Kyverno, ValidatingPolicy, все правила Deny):
- Kyverno 3.9.1 / v1.19.1 установлен (ns kyverno).
- Политика 1 применена на кластере как `require-resources-limits` (проверено: отказ на Pod и Deployment, ns monitoring исключён).
  TODO: поправить message (3 поля) в версии на ВМ; удалить случайный под `t` в ns monitoring (`kubectl -n monitoring delete pod t`);
  проверить, что применилось имя из файла (в выводе было `require-resources-limits`, в моём файле `require-resource-limits` — выровнять).
- Нарушитель violations/01-no-limits.yaml готов (в scratchpad; на ВМ — scp). 
- НЕ сделано: 00-compliant.yaml, политики 2–5 (студент пишет сам, подсказки даны), нарушители 02–05, общий прогон, скриншоты.
- Не присланы: `helm list -A`, `Allocated resources` (бюджет памяти), старый api из Лабы 2 (`helm uninstall api -n app`) — статус удаления неизвестен.
Дальше: 00-compliant → политика 2 + нарушитель 2 → ... → 5; затем Этап 2 (чарт chart/shop, label part-of!, imagePullPolicy IfNotPresent, graceful shutdown/preStop).
Договорённости: без вопросов-гейтов; файлы через scratchpad+scp; по одной политике за раз; пары «политика+нарушитель».

### 2026-10-05 — возобновление
- ВМ перезапускалась; кластер поднялся, всё Running. Путаница с Freelens — смотрел не тот namespace.
- `helm list -A`: старого api нет (удалён), остались kps, loki, alloy, jaeger, alert-webhook, karma, kyverno. Allocated resources всё ещё не присылал.
- `kubectl get vpol` → пусто, kyverno-resource-validating-webhook-cfg имел 0 webhooks: политика 1 пропала (вероятно удалена руками). Применена заново — ок.
- Случайный под `t` в monitoring (без лимитов, ns исключён) — удалить.
- Создан policies/violations/00-compliant.yaml (Pod compliant в shop; requests 50m/32Mi, limit 128Mi).
- По просьбе студента («пишем оставшиеся файлы») сгенерированы политики 02–05 и нарушители 02, 03, 03b (Deployment, проверка autogen), 04, 05, 05b (без тега). Файлы в scratchpad/policies/.
  Риски для проверки на ВМ: (1) optional-синтаксис `c.?securityContext.?privileged.orValue(false)` в 04 — если CEL-окружение Kyverno его не знает, заменить на цепочку has();
  (2) autogen для `object.metadata.labels` в 03 — проверяет 03b; (3) 04 запрещает только явный allowPrivilegeEscalation:true (неявный default в k8s = true, строже — требовать ==false);
  (4) CNPG-поды в Части 3: метки через inheritedMetadata, образы ghcr.io/cloudnative-pg/, лимиты через Cluster.spec.resources.
