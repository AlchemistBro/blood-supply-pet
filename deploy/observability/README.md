# Наблюдаемость: дашборды, алерты, provisioning Grafana

Телеметрия идёт в `grafana/otel-lgtm` (OTel Collector, Prometheus, Loki, Tempo и Grafana в одном образе, тег 0.11.0):
сервисы шлют трассы и метрики по OTLP (`BS_OTEL_ENDPOINT`, `libs/observability`). Здесь лежит то, что Grafana
подхватывает при старте: дашборды и правила алертов. Они хранятся как код: правки в интерфейсе Grafana не сохраняются
(`allowUiUpdates: false`), меняется файл в репозитории.

## Состав

```
deploy/observability/
  grafana/dashboards/                 JSON дашбордов (папка «АС распределения компонентов крови»)
    services-red.json                 АС: сервисы (RED) — запросы, 5xx, длительность, 401/403
    saga.json                         АС: сага подтверждения передачи — O07 по статусам, результаты, BROKEN, эскалации
    logistics-gateway.json            АС: шлюз перевозчика C03 — заказы, UNKNOWN/FAILED, попытки C03
    messaging.json                    АС: обмен сообщениями — outbox, лаг consumer-ов, DLT
    http-clients.json                 АС: исходящие HTTP-вызовы — httpx_client_calls_total по исходу
    audit.json                        АС: аудит и безопасность — цепочка хешей, отказы, подозрения на инцидент с ПДн
  grafana/provisioning/
    dashboards/blood-supply.yaml      провайдер: читает JSON из /var/lib/blood-supply/grafana/dashboards
    alerting/rules.yaml               правила алертов (группы messaging, integrations, security)
    alerting/contact-points.yaml      точка контакта ИТ-службы (демо-адрес) и маршрутизация
  kustomization.yaml                  те же файлы как ConfigMap для Kubernetes
```

Runbook-и, на которые ссылаются алерты: `docs/runbooks/` — инцидент с ПДн (24 ч / 72 ч), DLT, шлюз UNKNOWN/FAILED,
АИСТ-М недоступна, разрыв цепочки аудита. Обнаружение инцидентов с ПДн — ADR-0025.

## Алерты

| uid | Условие | Важность | Runbook |
| --- | --- | --- | --- |
| `bs-dlt-not-empty` | `increase(messaging_consumer_dead_letters_total[10m]) > 0` | critical | `dlt-messages.md` |
| `bs-consumer-lag` | `messaging_consumer_lag > 500` пять минут | warning | `dlt-messages.md` |
| `bs-outbox-stuck` | `messaging_outbox_oldest_age_seconds > 60` пять минут | critical | `dlt-messages.md` |
| `bs-inventory-breaker-open` | `httpx_client_calls_total{job="transfer",client="inventory",outcome="circuit_open"}` растёт | critical | `aist-unavailable.md` |
| `bs-gateway-unknown` | `logistics_transport_orders{status="UNKNOWN"} > 0` | critical | `gateway-unknown-failed.md` |
| `bs-gateway-failed` | `logistics_transport_orders{status="FAILED"} > 0` | critical | `gateway-unknown-failed.md` |
| `bs-audit-chain-broken` | `audit_chain_verify_ok < 1` | critical | `audit-chain-break.md` |
| `bs-audit-chain-not-verified` | последняя проверка цепочки старше часа | warning | `audit-chain-break.md` |
| `bs-pd-incident-suspected` | `increase(security_incidents_suspected_total[15m]) > 0` | critical | `pd-incident-24h-72h.md` |

Пороги демонстрационные (ADR-0010). У всех правил `noDataState: OK`: пока метрики нет, правило молчит, а когда
она появится — начинает работать без правки файла.

## Метрики

Имена — как их показывает Prometheus из OTLP: точки заменяются на `_`, счётчики получают `_total`, единица — суффикс
(`_seconds`); `job` — значение `service.name`.

| Метрика | Откуда | Состояние реализации на 26.10.2026 |
| --- | --- | --- |
| `http_server_request_duration_seconds_*` (`http_request_method`, `http_response_status_code`) | `otelhttp` в `libs/observability` | есть в main |
| `httpx_client_calls_total{client,outcome}` | `libs/httpx` (`httpx.client.calls`) | реализовано в main |
| `transfer_expiry_failures_total` | `services/transfer` (`transfer.expiry.failures`) | реализовано в main |
| `messaging_outbox_unpublished`, `messaging_outbox_oldest_age_seconds`, `messaging_outbox_published_total`, `messaging_outbox_failures_total` | хуки `RelayMetrics` в `libs/messaging` | **нет**: хуки не подключены к счётчикам; этап 8 |
| `messaging_consumer_handled_total`, `messaging_consumer_duration_seconds`, `messaging_consumer_dead_letters_total` | `libs/observability.ConsumerMetrics` | подключены к реальному consumer logistics-gateway; остальные consumers требуют отдельного подключения |
| `messaging_consumer_lag{group,topic}` | consumer (оффсеты группы) | **нет**: этап 8 |
| `transfer_obligations{status}`, `transfer_confirmations_total{result}`, `transfer_broken_total` | `services/transfer` | **нет**: этап 8 |
| `transfer_escalations_total{reason}` | `services/transfer` | **нет**: этап 7 (S7-WP2) |
| `logistics_transport_orders{status}`, `logistics_c03_attempts_total{outcome}` | `services/logistics-gateway` | **нет**: этап 8 |
| `audit_chain_verify_ok`, `audit_chain_verify_last_run_timestamp_seconds`, `security_incidents_suspected_total{rule}` | `services/audit/internal/app/metrics.go` | реализовано в main; историческая пометка об отсутствии устарела |

Панели с отсутствующей метрикой подписаны «(метрика появится в этапе N)» и показывают это же вместо данных.
Метрики Go-рантайма (`go_*`, `process_*`) регистрируются только в `/metrics` сервиса; otel-lgtm их не опрашивает,
поэтому на дашбордах их нет. В метки метрик не попадают логины, номера случаев и иные идентификаторы лиц.

## Подключение в compose

`deploy/compose/compose.yaml` не меняется этим пакетом. Чтобы otel-lgtm подхватил файлы, сервису `otel-lgtm`
добавляются тома (пути внутри образа — допущение по образу 0.11.0, см. ниже):

```yaml
  otel-lgtm:
    image: grafana/otel-lgtm:0.11.0
    environment:
      # Отправка алертов почтой — через Mailpit (C04 в демо).
      GF_SMTP_ENABLED: "true"
      GF_SMTP_HOST: mailpit:1025
      GF_SMTP_FROM_ADDRESS: grafana@demo.invalid
    volumes:
      - lgtmdata:/data
      - ../observability/grafana/dashboards:/var/lib/blood-supply/grafana/dashboards:ro
      - ../observability/grafana/provisioning/dashboards/blood-supply.yaml:/otel-lgtm/grafana/conf/provisioning/dashboards/blood-supply.yaml:ro
      - ../observability/grafana/provisioning/alerting/rules.yaml:/otel-lgtm/grafana/conf/provisioning/alerting/blood-supply-rules.yaml:ro
      - ../observability/grafana/provisioning/alerting/contact-points.yaml:/otel-lgtm/grafana/conf/provisioning/alerting/blood-supply-contact-points.yaml:ro
```

Файлы монтируются по одному, а не каталогом: каталог закрыл бы собственный provisioning образа (источники данных).

## Подключение в Kubernetes

В local/ci компонент sims задаёт SMTP Grafana на `mailpit.bs-sims.svc:1025`; отдельные правила разрешают
только этот поток от Grafana к Mailpit. Это настоящий путь демонстрационного оповещения, а не внешний
почтовый сервис. Kubernetes chaos проверяет точное DLT-сообщение, рост реального счётчика Prometheus,
срабатывание штатного правила для уникального type и письмо с этим type в Mailpit. Пустой результат
метрик, NoData/OK или одно наличие правила не считается успешной приёмкой.

`deploy/k8s/base/platform/observability/kustomization.yaml` включает этот каталог как ресурс: `configMapGenerator`
даёт ConfigMap `grafana-dashboards` и `grafana-provisioning` в `bs-observability`, Deployment `otel-lgtm` монтирует их
теми же путями (провайдер и правила — через `subPath`). Суффикс-хеш имени ConfigMap меняется вместе с содержимым,
поэтому правка дашборда перезапускает под и Grafana читает новую версию. Проверка сборки без кластера:

```
kubectl kustomize deploy/k8s/overlays/local
kubectl kustomize deploy/k8s/overlays/ci
```

## Допущения, которые нужно сверить при первом запуске

1. Каталог provisioning Grafana внутри образа `grafana/otel-lgtm:0.11.0` — `/otel-lgtm/grafana/conf/provisioning`.
   Сверить: `docker compose exec otel-lgtm ls /otel-lgtm/grafana/conf/provisioning`. Если путь другой — поправить
   его в compose-фрагменте выше и в `deploy/k8s/base/platform/observability/otel-lgtm.yaml`.
2. Uid источника данных Prometheus в образе — `prometheus` (правила алертов ссылаются на него напрямую; дашборды
   выбирают источник переменной `ds` и от uid не зависят). Сверить: Grafana → Connections → Data sources.
3. Метка `job` равна `service.name` сервиса (`transfer`, `logistics-gateway`, `audit` и т. д.) — так Prometheus
   переводит ресурс OTLP при пустом `service.namespace`.
4. Имя клиента `inventory` у `inventoryclient` в `transfer` (алерт предохранителя) — сверить с конфигурацией клиента.

## Как менять

Дашборд правится в JSON (или в Grafana с последующим экспортом «Export → Save to file» и заменой файла целиком);
`uid` дашборда не меняется — на него ссылаются ссылки и закладки. После правки: `python -c "import json,sys;
[json.load(open(f, encoding='utf-8')) for f in sys.argv[1:]]" deploy/observability/grafana/dashboards/*.json`
и сборка kustomize.
