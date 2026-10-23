<div align="center">

# 🐳 itmo-devops-labs

**Лабораторные работы по дисциплине «Контейнеризация и оркестрация приложений»**

ИТМО

![Docker](https://img.shields.io/badge/Docker-2496ED?style=flat&logo=docker&logoColor=white)
![Kubernetes](https://img.shields.io/badge/Kubernetes-326CE5?style=flat&logo=kubernetes&logoColor=white)
![Helm](https://img.shields.io/badge/Helm-0F1689?style=flat&logo=helm&logoColor=white)
![Go](https://img.shields.io/badge/Go-00ADD8?style=flat&logo=go&logoColor=white)
![Prometheus](https://img.shields.io/badge/Prometheus-E6522C?style=flat&logo=prometheus&logoColor=white)
![Grafana](https://img.shields.io/badge/Grafana-F46800?style=flat&logo=grafana&logoColor=white)
![Cilium](https://img.shields.io/badge/Cilium-F8C517?style=flat&logo=cilium&logoColor=black)
![n8n](https://img.shields.io/badge/n8n-EA4B71?style=flat&logo=n8n&logoColor=white)

[Курс](https://github.com/KeladKaal/containerization-and-orchestration/tree/main-rus) · [Лабы](#-лабораторные-работы) · [Стек](#-стек) · [Структура](#-структура-репозитория)

</div>

---

## 📚 Лабораторные работы

| № | Тема | Ключевые технологии | Статус |
|:-:|------|---------------------|:------:|
| 1 | [**Пишем свой докер**](./lab1) | Go, namespaces, cgroups, seccomp, unshare | ✅ Done |
| 2 | [**Наблюдаемость: метрики, логи, трейсы и алерты**](./lab2) | Prometheus, Grafana, Loki, Alloy, OpenTelemetry, Jaeger | ✅ Done |
| 3 | [**Собираем платформу для shop**](./lab3) | Helm, Kyverno, CloudNativePG, FreeLens | ✅ Done |
| 4 | [**SLA критического пути под перегрузкой**](./lab4) | Affinity/Spread, PriorityClass, preemption, KRR | ✅ Done |
| 5 | [**Zero-trust сеть с доказательством**](./lab5) | Cilium, Hubble, NetworkPolicy, chaos-тесты | ✅ Done |
| 6 | [**Автопилот релиза: катит хорошее, откатывает плохое**](./lab6) | Linkerd, mTLS, traffic split, canary | ✅ Done (🐸) |
| 7 | ~~MLOps~~ | — | ⛔ Не выполняется |
| 8 | [**Автоматизация devops-процесса с n8n + AI**](./lab8) | n8n, AI-агенты, GitHub, Prometheus, Grafana | ✅ Done |

> [!NOTE]
> Подробные отчёты со скриншотами — в `README.md` внутри каждой папки.

---

## 🔍 Кратко о каждой

<details>
<summary><b>Лаба 1 — Пишем свой докер</b></summary>

<br>

Собираем контейнер руками, без Docker: тестовая API на Go, изоляция через namespaces (`unshare`), ограничение ресурсов через cgroups, урезание прав и seccomp, собственный скрипт-«докер», сравнение образов (обычный vs multistage), гипервизор и стек мониторинга.

📄 [Отчёт](./lab1/README.md) · 📋 [Задание](https://github.com/KeladKaal/containerization-and-orchestration/blob/main-rus/lecture-1-docker/lab.md)
</details>

<details>
<summary><b>Лаба 2 — Наблюдаемость</b></summary>

<br>

Доставка сервиса в кластер через Helm и три столпа observability: метрики (Prometheus + Grafana), логи (Loki + Alloy), трейсы (OpenTelemetry + Jaeger), плюс алертинг.

📄 [Отчёт](./lab2/README.md) · 📋 [Задание](https://github.com/KeladKaal/containerization-and-orchestration/blob/main-rus/lecture-2-observability/lab.md)
</details>

<details>
<summary><b>Лаба 3 — Платформа для shop</b></summary>

<br>

Сервисы api и worker, ограждения кластера политиками Kyverno, единый Helm-чарт, Postgres через оператор CloudNativePG, эксперимент с падением control plane и мониторинг.

📄 [Отчёт](./lab3/README.md) · 📋 [Задание](https://github.com/KeladKaal/containerization-and-orchestration/blob/main-rus/lecture-3-kubernetes-control-plane/lab.md)
</details>

<details>
<summary><b>Лаба 4 — SLA под перегрузкой</b></summary>

<br>

Сервис batch на тесной площадке, разнесение подов по нодам, реальные requests/limits по данным KRR вместо угадывания, приоритеты и QoS, Pending и preemption, давление по памяти и проверка SLA критического пути под нагрузкой.

📄 [Отчёт](./lab4/README.md) · 📋 [Задание](https://github.com/KeladKaal/containerization-and-orchestration/blob/main-rus/lecture-4-scheduling-resources/lab.md)
</details>

<details>
<summary><b>Лаба 5 — Zero-trust сеть</b></summary>

<br>

Выбор CNI (Cilium), разбор того, как под получает сеть, непрерывность трафика через хаос, целевая матрица доступа, включение default-deny и её проверка на практике, поиск скрытой поломки через Hubble и мониторинг.

📄 [Отчёт](./lab5/README.md) · 📋 [Задание](https://github.com/KeladKaal/containerization-and-orchestration/blob/main-rus/lecture-5-kubernetes-networking/lab.md)
</details>

<details>
<summary><b>Лаба 6 — Автопилот релиза</b></summary>

<br>

Service mesh (Linkerd): baseline без mesh, mTLS, ретраи и таймауты «бесплатно», v1/v2 и traffic split, автопилот, который сам катит хороший релиз и откатывает плохой, цена mesh и мониторинг.

📄 [Отчёт](./lab6/README.md) · 📋 [Задание](https://github.com/KeladKaal/containerization-and-orchestration/blob/main-rus/lecture-6-service-mesh/lab.md)
</details>

<details>
<summary><b>Лаба 7 — MLOps</b> ⛔</summary>

<br>

Работы по теме MLOps нет в курсе.
</details>

<details>
<summary><b>Лаба 8 — n8n + AI</b></summary>

<br>

**«Страж персональных данных»** для медтех-стартапа: workflow в n8n на каждый PR ищет новые миграции и зависимости, запускает статический анализ, два AI-агента параллельно проверяют появление ПДн (152-ФЗ) и подтверждают уязвимости, а итоговый отчёт с уровнем риска публикуется комментарием в PR, с пингом юриста и блокировкой мерджа при высоком риске.

📄 [Отчёт](./lab8/README.md) · 📋 [Задание](https://github.com/KeladKaal/containerization-and-orchestration/tree/main-rus/lecture-8-ai)
</details>

---

## 🛠 Стек

| Область | Инструменты |
|---------|-------------|
| **Контейнеры** | Linux namespaces, cgroups v2, seccomp, Docker |
| **Оркестрация** | Kubernetes (minikube, kind), Helm |
| **Политики** | Kyverno |
| **Хранилище** | CloudNativePG (Postgres-оператор) |
| **Сеть** | Cilium, Hubble, NetworkPolicy |
| **Service mesh** | Linkerd |
| **Observability** | Prometheus, Grafana, Loki, Alloy, OpenTelemetry, Jaeger, Alertmanager |
| **Ресурсы** | KRR |
| **Автоматизация** | n8n, AI-агенты |
| **Языки** | Go, Bash |

---

## 📁 Структура репозитория

```
itmo-devops-labs/
├── lab1/   # свой докер: api, Dockerfile'ы, mydocker.sh
├── lab2/   # observability: api, helm, port-forward
├── lab3/   # платформа shop: chart, policies, install
├── lab4/   # SLA и планирование: batch, chart, отчёт KRR
├── lab5/   # zero-trust: chart, chaos/break-скрипты, loadgen
├── lab6/   # 🚧 service mesh: api, chart, gate, policies
└── lab8/   # n8n + AI: workflow, skills, стенд
```

В каждой папке есть `README.md` с отчётом, `screenshots/` с доказательствами.

---

<div align="center">

Сделано с ☕ и `kubectl get pods -A` · [Степанов Никита](https://github.com/Nist1)

</div>
