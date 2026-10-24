# Развёртывание в Kubernetes (kustomize)

Целевая среда по ADR-0009: kind локально и в CI, манифесты kustomize, PostgreSQL под CloudNativePG.
Спецификация — `docs/architecture/deployment.md`, задачи этапа — `docs/tasks/08-kubernetes.md`.
Сейчас здесь только платформа; сервисы приложения, ingress и NetworkPolicy добавляются отдельно.

## Что разворачивается

| Компонент | Namespace (сегмент) | Образ | Адрес внутри кластера |
| --- | --- | --- | --- |
| PostgreSQL 16, 2 экземпляра (CloudNativePG `Cluster pg`) | `bs-data` (данные) | `ghcr.io/cloudnative-pg/postgresql:16.9` | `pg-rw.bs-data.svc:5432` (запись), `pg-ro.bs-data.svc:5432` (реплика) |
| MinIO: архив WAL и базовые копии | `bs-data` | `minio/minio:RELEASE.2025-04-22T22-12-26Z` | `minio.bs-data.svc:9000`, консоль `:9001` |
| Kafka 3.9.1, один брокер KRaft | `bs-app` (основная сеть) | `apache/kafka-native:3.9.1` | `kafka.bs-app.svc:9092` |
| Job создания топиков | `bs-app` | `apache/kafka:3.9.1` | — |
| Keycloak 26.4, realm `blood-supply` | `bs-edge` (доступ) | `quay.io/keycloak/keycloak:26.4` | `keycloak.bs-edge.svc:8080/auth`, служебный `:9000` |
| grafana/otel-lgtm (OTel Collector, Prometheus, Loki, Tempo, Grafana) | `bs-observability` (контроль) | `grafana/otel-lgtm:0.11.0` | OTLP `otel-lgtm.bs-observability.svc:4317/4318`, Grafana `:3000` |
| Mailpit, имитатор почтового шлюза C04 (только local, ci) | `bs-sims` | `axllent/mailpit:v1.22` | SMTP `mailpit.bs-sims.svc:1025`, просмотр `:8025` |

Теги образов совпадают с `deploy/compose/compose.yaml`. Новые относительно compose: образы CloudNativePG,
MinIO и JVM-образ `apache/kafka:3.9.1` (в `kafka-native` нет утилиты `kafka-topics.sh`).

Базы и роли: `inventory`, `demand`, `planning`, `matching`, `transfer`, `logistics`, `notification`, `audit`
(объекты `Database` и `Cluster.spec.managed.roles`) и `keycloak` (создаётся при инициализации кластера).
Пароль каждой роли — в Secret `pg-role-<имя>` в `bs-data`.

Топики: 11 рабочих из `libs/contracts/contracts.go` (`AllTopics`) и 11 DLT (`<топик>.dlt`), список —
`base/platform/kafka/topics.txt`. Рабочие: 3 партиции, хранение 7 суток. DLT: 1 партиция, 30 суток.

## Структура

```
deploy/k8s/
  base/
    namespaces.yaml        bs-edge, bs-app, bs-data, bs-observability (метка blood-supply.io/segment)
    data/                  CloudNativePG Cluster, Database, ScheduledBackup; MinIO и Job бакета
    platform/kafka/        StatefulSet, сервисы, topics.txt и Job создания топиков
    platform/keycloak/     Deployment, realm ConfigMap (копия realm из compose), адрес KC_HOSTNAME
    platform/observability/ otel-lgtm; дашборды и алерты Grafana из deploy/observability (ConfigMap)
    kustomization.yaml     место под services/ и ingress/ отмечено комментарием
  components/
    demo-secrets/          ДЕМО-секреты (secretGenerator): роли PostgreSQL, MinIO, Keycloak
    sims/                  namespace bs-sims и Mailpit; сюда же — имитаторы C01, C02, C03
  overlays/
    local/                 base + демо-секреты + имитаторы
    ci/                    то же с урезанными ресурсами и томами
```

Секреты в `base` не генерируются: `prod-example` будет брать их из внешнего менеджера, а `local` и `ci`
подключают компонент `demo-secrets`. Значения в нём демонстрационные (пароль роли равен её имени, как в compose)
и помечены меткой `blood-supply.io/demo=true`.

## Проверка сборки

Без кластера и без сети:

```
kubectl kustomize deploy/k8s/overlays/local
kubectl kustomize deploy/k8s/overlays/ci
```

Удалённых ресурсов в kustomization нет, сборка работает офлайн.

## Порядок развёртывания

Выполняется автоматизацией `task k8s:up` (её ещё нет); вручную:

1. Оператор CloudNativePG **1.27.0** (CRD в репозиторий не кладём, ставятся из релиза оператора):

   ```
   kubectl apply --server-side -f \
     https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.27/releases/cnpg-1.27.0.yaml
   kubectl rollout status deployment -n cnpg-system cnpg-controller-manager --timeout=180s
   ```

   Без CRD `postgresql.cnpg.io/v1` применение overlay упадёт на `Cluster`, `Database`, `ScheduledBackup`.
2. Overlay: `kubectl apply --server-side -k deploy/k8s/overlays/local`.
   Дальше всё сходится само, в таком порядке:
   - namespace и секреты; MinIO; Job `minio-setup` ждёт MinIO, создаёт бакет `pg-backups` с версионированием и
     учётную запись `pg-backup` с доступом только к нему;
   - `Cluster pg`: primary, затем реплика; архив WAL начинает уходить в MinIO; `ScheduledBackup pg-daily`
     с `immediate: true` сразу снимает первую базовую копию; объекты `Database` создают базы сервисов;
   - Keycloak ждёт базу `keycloak` (перезапускается, пока `pg-rw` не готов), импортирует realm;
   - Kafka; Job `kafka-topics` ждёт брокер и создаёт топики.
3. Ожидание готовности:

   ```
   kubectl wait --for=condition=Ready cluster/pg -n bs-data --timeout=600s
   kubectl wait --for=condition=complete job/minio-setup -n bs-data --timeout=300s
   kubectl wait --for=condition=complete job/kafka-topics -n bs-app --timeout=300s
   kubectl rollout status deployment/keycloak -n bs-edge --timeout=600s
   ```

Сервисы приложения должны стартовать после Job `kafka-topics`: сервис сам вызывает `messaging.EnsureTopics`
с одной партицией, и если он успеет первым, топик останется с одной партицией.

Образы для kind заранее загружаются в узлы (`kind load docker-image …`), список — таблица выше и образ
оператора `ghcr.io/cloudnative-pg/cloudnative-pg:1.27.0`.

## Что нужно знать сервисам и следующим манифестам

- `BS_DB_URL`: `postgres://<роль>:<пароль>@pg-rw.bs-data.svc:5432/<база>?sslmode=require`.
  CloudNativePG включает TLS на сервере; без TLS подключение тоже пройдёт (`host` в pg_hba), но в кластере его
  стоит требовать. Secret `pg-role-*` лежит в `bs-data` и из `bs-app` не виден: сервису нужен свой Secret
  в `bs-app` с тем же паролем (в демо — генератор с теми же значениями).
- `BS_KAFKA_BROKERS`: `kafka.bs-app.svc:9092`.
- `BS_OIDC_ISSUER`: `https://localhost/auth/realms/blood-supply` (значение `KC_HOSTNAME` + путь realm);
  `BS_OIDC_JWKS_URL`: `http://keycloak.bs-edge.svc:8080/auth/realms/blood-supply/protocol/openid-connect/certs`.
  В compose у Keycloak нет префикса `/auth`; в k8s он нужен, чтобы ingress отдавал `/auth` Keycloak на том же хосте.
- `BS_OTEL_ENDPOINT`: `otel-lgtm.bs-observability.svc:4318`. `BS_SMTP_ADDR`: `mailpit.bs-sims.svc:1025`.
- NetworkPolicy должна пропускать, кроме `bs-app → bs-data`:
  Keycloak (`bs-edge`) → `pg-rw:5432` (его база в общем кластере); оператор `cnpg-system` → поды `pg` (порт 8000)
  и поды `pg` между собой (5432, репликация); поды `pg` → MinIO (9000); Job `minio-setup` → MinIO;
  все сегменты → `bs-observability` (4317/4318); `bs-app` → `bs-sims`.

## Резервное копирование и PITR (NFR005–NFR007, NFR012)

- Непрерывный архив WAL в `s3://pg-backups/` (MinIO), сжатие gzip.
- `archive_timeout = 5min`: даже без нагрузки сегмент WAL уходит в архив не реже раза в 5 минут, поэтому
  потеря данных при полной утрате кластера — не больше 5 минут (цель NFR007 — 15 минут).
- Базовая копия раз в сутки (`ScheduledBackup pg-daily`, 01:00 UTC) и сразу после создания кластера.
- `retentionPolicy: 7d`: восстановление на любой момент за последние 7 суток.
- Реплика (`instances: 2`) закрывает отказ узла без восстановления из копии (NFR005).
- Учение восстановления (удаление primary, восстановление на `targetTime`, замер RTO и RPO) — отдельная задача этапа.

## Допущения и ограничения

1. **Изоляция баз в одном кластере PostgreSQL.** NetworkPolicy работает на уровне пода и порта: под, которому
   открыт `pg-rw:5432`, видит сервер целиком, а не одну базу. Поэтому разделение баз держится на учётных данных:
   роль на сервис, `pg_hba` пускает роль только в её базу и отклоняет всё остальное (`host all all all reject`) —
   это строже `REVOKE CONNECT … FROM PUBLIC` из compose. Утечка пароля чужой роли или уязвимость сервера
   этим не закрываются. Для данных класса K1 (`matching`, `clinical_request_patient`) предлагается
   **отдельный кластер CloudNativePG** `pg-k1` с NetworkPolicy, открывающей порт только подам `matching`,
   своим бакетом и учётной записью копий; цена — ещё два пода PostgreSQL. Рекомендация: отдельный кластер
   в `prod-example`, общий кластер с pg_hba в `local` и `ci`. Учесть: номер случая до квитанции АИСТ-М лежит
   и в `inventory` (`aist_outbox_message.payload`, security.md), так что `inventory` тоже кандидат.
2. **Встроенный barmanObjectStore устарел** в CloudNativePG 1.26+ (замена — Barman Cloud Plugin: объект
   `ObjectStore` и `spec.plugins`). В 1.27 он работает; при переходе оператора на версию, где его уберут,
   `backup` и `ScheduledBackup.method` переводятся на плагин. Образ PostgreSQL обязан содержать `barman-cloud`
   (вариант system; тег `16.9` — он).
3. **Keycloak** в режиме `start` без `--optimized` пересобирает себя при запуске, поэтому корень ФС записываемый.
   Read-only FS даст собственный образ с `kc.sh build`. Одна реплика с локальным кешем сессий.
4. **otel-lgtm** работает от root, поэтому `bs-observability` — Pod Security `baseline`, у контейнера
   оставлен минимальный набор capabilities. Отдельный OTel Collector с удалением атрибута `patient_case_no`
   (deployment.md) не развёрнут, как и в compose: сервисы не пишут номер случая в трассы, коллектор —
   вторая линия защиты, кандидат на добавление.
5. **Kafka**: один брокер без аутентификации, корень ФС записываемый (скрипт образа пишет `server.properties`).
   Для реального развёртывания — Strimzi, SASL/SCRAM и ACL по владельцу топика (security.md).
6. **Секреты без хеш-суффикса**: на них ссылаются CRD CloudNativePG, а kustomize не переписывает имена в CRD.
   Смена значения не перезапускает Keycloak сама; роли PostgreSQL оператор перечитывает по метке `cnpg.io/reload`.
7. **Job неизменяемы**: при правке `topics.txt` или скрипта меняется хеш ConfigMap и шаблон Job, повторный
   `apply` будет отклонён. Перед ним: `kubectl delete job kafka-topics -n bs-app` (то же для `minio-setup`).
   Существующим топикам Job партиции и хранение не меняет.
8. **Realm** лежит в двух местах (`deploy/compose/keycloak` и `base/platform/keycloak`): kustomize не читает файлы
   вне своего каталога без `--load-restrictor`. Правка realm — в обоих файлах; сверку `cmp` стоит добавить в CI.
9. **MinIO**: образ закреплён на последнем выпуске с публичной сборкой в Docker Hub; клиент `mc` берётся из того же
   образа. При замене образа — проверить наличие `mc` или вынести Job на `minio/mc`.
10. **Роль `audit_writer`** (только INSERT, security.md) не создана: сервис `audit` пока её не использует.
11. **Пробы**: у Kafka и otel-lgtm — TCP и HTTP-проверки порта; пробы сервисов (`/readyz`, `/healthz`) задаются
    в их манифестах.
12. Ни один образ и ни один CRD при подготовке манифестов не скачивались; теги образов `minio/minio`,
    `ghcr.io/cloudnative-pg/postgresql:16.9` и `apache/kafka:3.9.1` проверяются при первой загрузке.
