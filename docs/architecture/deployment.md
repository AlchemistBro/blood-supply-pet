# Развёртывание: compose для разработки, Kubernetes как целевая среда

## Сетевые сегменты (из ПР4 АМ, перенесены в k8s как NetworkPolicy)

```mermaid
flowchart TB
    subgraph untrusted[Сети клиник и смежных систем · ЗСПД / VPN]
        users[Пользователи]
        c01[C01] 
        c02[C02]
        c03[C03]
        c04[C04]
    end
    subgraph access[Сегмент доступа · namespace bs-edge]
        ingress[ingress-nginx · TLS]
        keycloak[Keycloak]
    end
    subgraph core[Основная сеть · namespace bs-app]
        web[web]
        svcs[inventory · demand · planning · matching · transfer · logistics-gateway · notification · audit]
        kafka[Kafka]
    end
    subgraph data[Сегмент данных · namespace bs-data]
        pg[(PostgreSQL · CloudNativePG)]
        backup[(WAL-архив и копии · MinIO)]
    end
    subgraph control[Зона контроля · namespace bs-observability]
        otel[OTel Collector]
        lgtm[grafana/otel-lgtm]
    end
    users --> ingress --> web
    users -.OIDC.-> keycloak
    c02 --> ingress --> svcs
    web --> svcs
    svcs <--> kafka
    svcs --> pg
    pg --> backup
    svcs -.телеметрия.-> otel --> lgtm
    svcs --> c01
    svcs --> c03
    svcs --> c04
```

NetworkPolicy: `bs-data` принимает только из `bs-app`; `bs-observability` только принимает; `matching` (K1) имеет
отдельную политику — к его базе ходит только он. Поток F-06 (ИНТ-05, K1) в проде — mTLS, в демо — обычный HTTP к имитатору с пометкой.

## Docker Compose (`deploy/compose`)

| Файл | Содержимое |
| --- | --- |
| `compose.yaml` | postgres (init: базы и роли), kafka (KRaft), keycloak (realm import), otel-lgtm, mailpit, nginx; профили `services` (все Go-сервисы из образов), `sims` (имитаторы), `ui` (kafka-ui) |
| `compose.dev.yaml` | только платформа; сервисы запускаются локально через `task dev:<svc>` |
| `postgres/init.sql` | `CREATE ROLE/DATABASE` на каждый сервис, `REVOKE CONNECT FROM PUBLIC` |
| `keycloak/realm-blood-supply.json` | realm, клиент `web` (confidential, PKCE), роли, атрибут `clinic_id`, демо-пользователи: `physician1`, `operator-c001`, `operator-c002`, `analyst1`, `duty1`, `it1` (пароль `demo`) |
| `nginx/nginx.conf` | TLS с самоподписанным сертификатом, `/` → web, `/auth` → keycloak, `/api/int/forecast` → planning (ИНТ-02) |
| `otel/collector.yaml` | приём OTLP, маскирование атрибутов `patient_case_no`, экспорт в lgtm |

Порты: web 8080, keycloak 8180, kafka 9092, postgres 5432, grafana 3000, mailpit 8025, kafka-ui 8090, сервисы 8101–8108, имитаторы 8201–8203.

## Kubernetes (`deploy/k8s`)

```
deploy/k8s/
  base/
    namespaces.yaml
    platform/   kafka (StatefulSet, 1 брокер), keycloak (Deployment + realm ConfigMap), otel-lgtm
    data/       cnpg Cluster (instances: 2, backup → MinIO, retention 7d), ScheduledBackup, Database/Role на сервис
    services/   девять Deployment/Service/PDB, три HPA; общий стандарт в components/go-service
    networkpolicy/  сегменты и отдельные правила для сервисов с данными K1
    ingress/    Ingress для web, ИНТ-02 и Keycloak, Exact-маршруты BFF /auth/login и /auth/callback
    kustomization.yaml
  overlays/
    local/      kind: NodePort, самоподписанный TLS, демо-секреты (secretGenerator), имитаторы включены
    ci/         то же, минимальные ресурсы, проверки с runner через port-forward и ingress
    prod-example/  пример: реплики 2, ресурсы, внешний Secret-менеджер, без имитаторов (не разворачивается)
```

Стандарт сервиса в k8s: `readinessProbe /readyz` (БД и Kafka достижимы, миграции применены), `livenessProbe /healthz`,
`resources` заданы, `securityContext` без root и с read-only FS, `PodDisruptionBudget`, HPA по CPU для stateless
(web, inventory, matching), для `transfer`/`logistics-gateway` масштабирование допустимо (аренда и CAS это разрешают),
ретранслятор outbox один на сервис через advisory lock.

CloudNativePG: `Cluster` с 2 экземплярами (primary + replica), непрерывный архив WAL в MinIO (`barmanObjectStore`),
Retention копий — 7 дней. Учение восстановления (`task k8s:drill`): создаёт прикладную заявку и SQL-маркеры,
проверяет завершённую копию и архив WAL, удаляет primary и восстанавливает отдельный `pg-drill` на `targetTime`.
После проверки маркеров девять подключений к БД временно переводятся на копию: проверяются чтение и расшифровка
старой заявки, новая запись только в копию, здоровая сага с перевозкой, readiness и OIDC-вход трёх ролей.
Исходные подключения возвращаются даже при ошибке проверки. RTO и интервал до цели измеряются отдельно;
контролируемый интервал не является общей гарантией RPO.

`up.sh` устанавливает metrics-server для CPU HPA; на тестовом kind допускается kubelet TLS с самоподписанным
сертификатом. После запуска проверяются `ScalingActive` всех трёх HPA и реальный ответ metrics API.
Сетевое разделение баз внутри общего PostgreSQL обеспечивают роли и `pg_hba`, а не NetworkPolicy на отдельную БД.

Исходники можно собрать во время `task k8s:up`. Для готового релиза задаётся `BS_K8S_RELEASE_MANIFEST`:
все 13 образов фиксируются по digest, приватный GHCR использует `BS_K8S_REGISTRY_CONFIG`. Время такого запуска
включает создание kind, загрузку образов, сетевую проверку, готовность платформы, сервисов и HPA; предел — 600 с.
Полная сборка исходников учитывается отдельным режимом. [Публикация и проверка релиза](../release.md).

## Конфигурация сервисов

Переменные окружения (12-factor), префикс `BS_`: `BS_DB_URL`, `BS_KAFKA_BROKERS`, `BS_OIDC_ISSUER`, `BS_OIDC_AUDIENCE`,
`BS_OTEL_ENDPOINT`, `BS_HTTP_ADDR`, `BS_LOG_LEVEL`, сервис-специфичные `BS_AIST_URL`, `BS_C03_URL`, `BS_SMTP_ADDR`,
`BS_INVENTORY_URL` (для transfer). Секреты только через переменные из Secret/`.env`.

## Образы

Многостадийный Dockerfile один на все сервисы с `ARG SERVICE`; финальный образ
`gcr.io/distroless/static-debian12:nonroot`, пользователь 65532, версия в `libs/runtime.Version`.
Кандидат содержит 12 сервисов и отдельный MinIO из официальных исходников. После проверок выпускаются
версионные ссылки GHCR с теми же digest, подписями cosign, SPDX SBOM и provenance; состав — в `images.json`.
