# Развёртывание в Kubernetes (kustomize)

Целевая среда по ADR-0009: kind локально и в CI, манифесты kustomize, PostgreSQL под CloudNativePG.
Спецификация — `docs/architecture/deployment.md`, задачи этапа — `docs/tasks/08-kubernetes.md`.
Здесь платформа, сервисы приложения, имитаторы, вход (Ingress, TLS) и NetworkPolicy по сегментам;
overlays `local`, `ci` и `prod-example`.

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
    platform/observability/ otel-lgtm
    services/              <svc>/ deployment, service, pdb, hpa; общие ConfigMap окружения
    ingress/               Ingress web, Keycloak (/auth), ИНТ-02; закрытые служебные пути
    networkpolicy/         запрет по умолчанию, DNS, потоки по сегментам, отдельные правила K1
  components/
    go-service/            стандарт пода сервиса на Go: пробы, securityContext, /tmp, Downward API
    demo-secrets/          ДЕМО-секреты (secretGenerator): роли PostgreSQL, MinIO, Keycloak, сервисы
    sims/                  namespace bs-sims, Mailpit, sim-aist, sim-emergency, sim-logistics, их NetworkPolicy
  overlays/
    local/                 base + демо-секреты + имитаторы
    ci/                    то же с урезанными ресурсами и томами
    prod-example/          без демо-секретов и имитаторов: заглушки секретов, домен, внешние системы
```

Секреты в `base` не генерируются: `prod-example` берёт их из внешнего менеджера (заглушки), а `local` и `ci`
подключают компонент `demo-secrets`. Значения в нём демонстрационные (пароль роли равен её имени, как в compose)
и помечены меткой `blood-supply.io/demo=true`.

## Проверка сборки

Без кластера и без сети:

```
kubectl kustomize deploy/k8s/overlays/local
kubectl kustomize deploy/k8s/overlays/ci
kubectl kustomize deploy/k8s/overlays/prod-example
```

Удалённых ресурсов в kustomization нет, сборка работает офлайн.

Стандарт сервиса, ссылки на ConfigMap/Secret и политику запрета по умолчанию в каждом namespace проверяет
`tools/k8scheck` (тоже без кластера):

```
kubectl kustomize deploy/k8s/overlays/local | go run ./tools/k8scheck
```

## Порядок развёртывания

Выполняется автоматизацией `task k8s:up` (её ещё нет); вручную:

1. Оператор CloudNativePG **1.27.0** (CRD в репозиторий не кладём, ставятся из релиза оператора):

   ```
   kubectl apply --server-side -f \
     https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.27/releases/cnpg-1.27.0.yaml
   kubectl rollout status deployment -n cnpg-system cnpg-controller-manager --timeout=180s
   ```

   Без CRD `postgresql.cnpg.io/v1` применение overlay упадёт на `Cluster`, `Database`, `ScheduledBackup`.

   Контроллер ingress-nginx для kind (namespace `ingress-nginx`, класс `nginx`, порты 80/443 узла):

   ```
   kubectl apply -f \
     https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.11.3/deploy/static/provider/kind/deploy.yaml
   kubectl wait -n ingress-nginx --for=condition=Ready pod -l app.kubernetes.io/component=controller --timeout=180s
   ```

   CNI кластера должен исполнять NetworkPolicy (kindnet в kind 0.24+ это делает; иначе политики молча
   не действуют). Секрет TLS `bs-tls` — раздел «Вход и TLS».
2. Overlay: `kubectl apply --server-side -k deploy/k8s/overlays/local`.
   Дальше всё сходится само, в таком порядке:
   - namespace и секреты; MinIO; Job `minio-setup` ждёт MinIO, создаёт бакет `pg-backups` с версионированием и
     учётную запись `pg-backup` с доступом только к нему;
   - `Cluster pg`: primary, затем реплика; архив WAL начинает уходить в MinIO; `ScheduledBackup pg-daily`
     с `immediate: true` сразу снимает первую базовую копию; объекты `Database` создают базы сервисов;
   - Keycloak ждёт базу `keycloak` (перезапускается, пока `pg-rw` не готов), импортирует realm;
   - Kafka; Job `kafka-topics` ждёт брокер и создаёт топики;
   - сервисы перезапускаются, пока недоступны база или Kafka, затем применяют миграции и становятся готовыми
     (`/readyz`); имитаторы готовы сразу.
3. Ожидание готовности:

   ```
   kubectl wait --for=condition=Ready cluster/pg -n bs-data --timeout=600s
   kubectl wait --for=condition=complete job/minio-setup -n bs-data --timeout=300s
   kubectl wait --for=condition=complete job/kafka-topics -n bs-app --timeout=300s
   kubectl rollout status deployment/keycloak -n bs-edge --timeout=600s
   kubectl wait --for=condition=Available deployment --all -n bs-app --timeout=600s
   ```

Сервисы должны создать свои топики позже Job `kafka-topics`: сервис сам вызывает `messaging.EnsureTopics`
с одной партицией, и если успеет первым, топик останется с одной партицией. На практике сервис доходит
до Kafka только после базы и миграций, а `Cluster pg` готовится дольше брокера; надёжный порядок даст
`task k8s:up` (применить платформу, дождаться Job, затем сервисы).

Образы для kind заранее загружаются в узлы (`kind load docker-image …`), список — таблица выше и образ
оператора `ghcr.io/cloudnative-pg/cloudnative-pg:1.27.0`.

## Сервисы приложения

Образы `ghcr.io/alchemistbro/blood-supply-<сервис>:edge` (deploy/docker/Dockerfile: distroless static,
пользователь 65532). Сервис слушает порт из `docs/PLAN.md` (`BS_HTTP_ADDR`), Service отдаёт его же.

Имена переменных сверены с кодом сервисов (в скобках — где они читаются). Где кода ещё нет, стоит пометка
«уточнить при реализации»: имя взято из `docs/architecture/deployment.md`.

| Сервис | Namespace | Порт | База и роль | Реплики | Особые переменные |
| --- | --- | --- | --- | --- | --- |
| `web` | `bs-app` | 8080 | — (ADR-0018) | HPA 2–3 | ConfigMap `web-config`: `BS_WEB_PUBLIC_URL`, `BS_SESSION_TTL`, `BS_COOKIE_SECURE`, `BS_HSTS_MAX_AGE`, `BS_DEMO_ACCESS`; `BS_OIDC_CLIENT_ID`, `BS_OIDC_CLIENT_SECRET`, `BS_SESSION_KEY`, `BS_SESSION_KEY_PREVIOUS` (необязателен), служебный клиент, `BS_AUDIT_URL` (`services/web/cmd/web`) |
| `inventory` | `bs-app` | 8101 | `inventory` | HPA 2–3 | `BS_AIST_URL`, `BS_AIST_POLL_INTERVAL`, `BS_AIST_PAGE_LIMIT`, `BS_AIST_STATUS_TIMEOUT` (`internal/config`), `BS_K1_KEYS` (`libs/k1seal`) |
| `demand` | `bs-app` | 8102 | `demand` | 1 | только общие; уточнить при реализации |
| `planning` | `bs-app` | 8103 | `planning` | 1 | только общие (ИНТ-02 принимает по HTTP); уточнить при реализации |
| `matching` | `bs-app` | 8104 | `matching` | HPA 2–3 | `BS_K1_KEYS` (`libs/k1seal`); прочее уточнить при реализации |
| `transfer` | `bs-app` | 8105 | `transfer` | 1 | `BS_INVENTORY_URL`, `BS_INVENTORY_TIMEOUT`, служебный клиент и `BS_SERVICE_TOKEN_URL` (`adapters/inventoryclient`, ADR-0004); `BS_EXPIRY_INTERVAL`, `BS_ALLOW_COPY_CONFIRM_EMERGENCY` (`internal/app`) |
| `logistics-gateway` | `bs-app` | 8106 | `logistics` | 1 | `BS_C03_URL`, `BS_C03_TIMEOUT`, `BS_LOGISTICS_LEASE`, `BS_LOGISTICS_MAX_ATTEMPTS`, `BS_LOGISTICS_RECONCILER_ENABLED` (`cmd/logistics-gateway/config.go`) |
| `notification` | `bs-app` | 8107 | `notification` | 1 | `BS_SMTP_ADDR` — уточнить при реализации отправки (ADR-0021); `BS_NOTIFY_RECIPIENTS_FILE` не задан — встроенный справочник |
| `audit` | `bs-app` | 8108 | `audit` | 1 | только общие |
| `sim-aist` (C01) | `bs-sims` | 8201 | — | 1 | `BS_LOG_LEVEL`, `BS_LOG_FORMAT` (`internal/config`) |
| `sim-emergency` (C02) | `bs-sims` | 8202 | — | 1 | `BS_PLANNING_URL` — база planning, путь `/int/forecast` добавляется (`internal/config`) |
| `sim-logistics` (C03) | `bs-sims` | 8203 | — | 1 | `BS_ENV`, `BS_OTEL_ENDPOINT` (`cmd/sim-logistics`) |

Явно заданные значения настроек (`BS_AIST_*`, `BS_C03_TIMEOUT`, `BS_LOGISTICS_*`, `BS_EXPIRY_INTERVAL`) равны
умолчаниям в коде: они вынесены, чтобы их было видно и можно было менять overlay-ем.

### Стандарт пода (`components/go-service`)

Компонент накладывает патч на каждый Deployment с меткой `blood-supply.io/runtime=go-service`:
`startupProbe` и `livenessProbe` на `/healthz`, `readinessProbe` на `/readyz`; `runAsNonRoot` (65532),
`readOnlyRootFilesystem`, `drop: [ALL]`, seccomp `RuntimeDefault`, без токена ServiceAccount и без
переменных ссылок на сервисы; `/tmp` — `emptyDir`; `POD_NAME`, `POD_NAMESPACE` через Downward API;
`RollingUpdate` с `maxUnavailable: 0`; аннотации `prometheus.io/scrape|path|port` (ServiceMonitor не
используется: CRD Prometheus Operator в кластере нет; метрики и так уходят по OTLP в otel-lgtm).
Условия: контейнер называется `app`, порт — `http`; ресурсы и реплики задаёт манифест сервиса.

Каждому сервису — PodDisruptionBudget `maxUnavailable: 1` (при одной реплике не мешает обслуживанию узла).
HPA по CPU (70 %) у `web`, `inventory`, `matching` (docs/tasks/08-kubernetes.md). `maxReplicas: 3` —
по числу партиций рабочих топиков: потребители одной группы сверх числа партиций простаивают, а если топик
успел создать сам сервис (одна партиция), читает одна реплика, остальные в резерве. В `ci` `minReplicas: 1`.

### Переменные окружения

Общие — ConfigMap `platform-env` (`envFrom`, `base/services/kustomization.yaml`):

| Переменная | Значение |
| --- | --- |
| `BS_ENV` | `k8s` в base, `local` / `ci` в overlay |
| `BS_LOG_LEVEL`, `BS_LOG_FORMAT` | `info`, `json` |
| `BS_KAFKA_BROKERS` | `kafka.bs-app.svc:9092` |
| `BS_OIDC_ISSUER` | `https://localhost/auth/realms/blood-supply` — внешний адрес Keycloak (`KC_HOSTNAME`) + realm |
| `BS_OIDC_JWKS_URL` | `http://keycloak.bs-edge.svc:8080/auth/realms/blood-supply/protocol/openid-connect/certs` |
| `BS_OIDC_DISCOVERY_URL` | `http://keycloak.bs-edge.svc:8080/auth/realms/blood-supply` — discovery для `web` изнутри кластера, из него же `web` берёт адрес токенов |
| `BS_SERVICE_TOKEN_URL` | `http://keycloak.bs-edge.svc:8080/auth/realms/blood-supply/protocol/openid-connect/token` — служебный токен `transfer` (без него адрес выводится из издателя, а внешний `https://localhost` из пода недоступен) |
| `BS_OIDC_AUDIENCE` | `blood-supply-api` |
| `BS_OTEL_ENDPOINT` | `otel-lgtm.bs-observability.svc:4318` |

`BS_HTTP_ADDR` (`libs/runtime`, по умолчанию `:8080`) задаётся явно в каждом Deployment: сервис слушает порт из PLAN.
В compose у Keycloak нет префикса `/auth`; в k8s он нужен, чтобы ingress отдавал `/auth` Keycloak на том же хосте.

Адреса — ConfigMap `service-endpoints` (`BS_<СЕРВИС>_URL`) и `external-systems`
(`BS_AIST_URL`, `BS_C03_URL`, `BS_SMTP_ADDR`; на стенде указывают на имитаторы в `bs-sims`). Сервис берёт
только нужные ключи (`configMapKeyRef`). Имитаторы читают свой ConfigMap `sims-env` в `bs-sims`.

`web-config` в `local` и `ci`: `BS_HSTS_MAX_AGE=0` (самоподписанный сертификат на `localhost` не закрепляется
в браузере) и `BS_DEMO_ACCESS=true` (блок демо-учёток на странице входа); в base — HSTS год и без демо-доступа.

Секреты (в `local`/`ci` — `components/demo-secrets`, в реальном развёртывании — внешний менеджер):

| Secret (`bs-app`) | Ключи | Кому |
| --- | --- | --- |
| `<сервис>-db` | `BS_DB_URL` = `postgres://<роль>:<пароль>@pg-rw.bs-data.svc:5432/<база>?sslmode=require` | сервису с базой |
| `web-secrets` | `BS_OIDC_CLIENT_SECRET`, `BS_SESSION_KEY` (32 байта, base64), при ротации `BS_SESSION_KEY_PREVIOUS` | `web` |
| `service-client` | `BS_SERVICE_CLIENT_ID`, `BS_SERVICE_CLIENT_SECRET` (клиент `services`, client credentials) | `web` (аудит, ADR-0017), `transfer` (ADR-0004) |
| `k1-keys` | `BS_K1_KEYS` (`идентификатор:ключ-base64`, через запятую для ротации) | только `matching` и `inventory` (ADR-0020) |

Пароль в `<сервис>-db` совпадает с `pg-role-<роль>` в `bs-data`: секреты между namespace не видны, поэтому
значение хранится в двух местах. CloudNativePG включает TLS на сервере; `sslmode=require` шифрует канал без
проверки сертификата (CA оператора в демо не раздаётся).

## Вход и TLS (`base/ingress`)

ingress-nginx, класс `nginx`, хост `localhost` (в `prod-example` — домен). Снаружи доступны только:

| Путь | Куда | Ограничения |
| --- | --- | --- |
| `/` | `web:8080` (`bs-app`) | 20 запросов/с с адреса (всплеск 60), тело до 5 МБ (CSV оценки расхода), SSE без буферизации, таймаут чтения 1 ч |
| `/auth` | `keycloak:8080` (`bs-edge`) | 10 запросов/с с адреса (подбор паролей), буфер заголовков 128 КБ |
| `/api/int/forecast` | `planning:8103`, путь `/int/forecast` (ИНТ-02) | фильтр адресов источника (в демо — частные сети, в `prod-example` — адрес шлюза C02), служебный токен (проверяет planning), 5 запросов/с, тело до 1 МБ |

Закрыты снаружи (отдельные Ingress с допуском только `127.0.0.1`, то есть никому извне): служебные пути web
`/metrics`, `/healthz`, `/readyz`, `/openapi.yaml`; консоль администратора Keycloak `/auth/admin` и realm `master`.
API остальных сервисов в Ingress нет: внутрь кластера запрос попадает только через web, Keycloak и ИНТ-02.
Администрирование Keycloak и просмотр Grafana — `kubectl port-forward`.

Для ИНТ-02 в реальном развёртывании добавляется mTLS клиента C02: аннотации
`nginx.ingress.kubernetes.io/auth-tls-secret: bs-app/c02-client-ca` и `auth-tls-verify-client: "on"`.

**TLS.** Ingress ссылаются на секрет `bs-tls` (тип `kubernetes.io/tls`) в `bs-app` и `bs-edge`. Ключ в репозитории
не хранится (секреты в git запрещены, в CI работает gitleaks), поэтому secretGenerator для него не используется.
Самоподписанный демо-сертификат создаётся при развёртывании:

```
openssl req -x509 -nodes -newkey rsa:2048 -days 365 -subj "/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" -keyout tls.key -out tls.crt
kubectl create secret tls bs-tls -n bs-app  --cert tls.crt --key tls.key
kubectl create secret tls bs-tls -n bs-edge --cert tls.crt --key tls.key
```

Пока секрета нет, ingress-nginx отдаёт свой самоподписанный сертификат по умолчанию — стенд работает, браузер
предупреждает так же. В `prod-example` сертификат выпускает внешний УЦ (cert-manager в демо не используется:
это ещё один набор CRD).

## NetworkPolicy (`base/networkpolicy`, `components/sims/networkpolicy.yaml`)

В каждом namespace — `default-deny` на вход и выход для всех подов и `allow-dns` (CoreDNS в `kube-system`).
Каждый поток открыт с двух сторон: выход у источника, вход у получателя.

| Откуда | Куда | Порт |
| --- | --- | --- |
| ingress-nginx | `web`; `planning` (ИНТ-02); Keycloak | 8080; 8103; 8080 |
| сервисы `bs-app` | Kafka | 9092 |
| сервисы `bs-app` | PostgreSQL `pg` (`bs-data`) — только этот порт | 5432 |
| сервисы `bs-app` | Keycloak (`bs-edge`): JWKS, discovery, служебные токены | 8080 |
| сервисы `bs-app` | внутренние API друг друга (`web → audit`, `transfer → inventory`, …) | 8101–8108 |
| сервисы `bs-app` | otel-lgtm (`bs-observability`) | 4317, 4318 |
| `logistics-gateway`, `notification` | `sim-logistics`, Mailpit (`bs-sims`) | 8203, 1025 |
| `inventory` | `sim-aist` (`bs-sims`) | 8201 |
| `sim-emergency` | `planning` (ИНТ-02) | 8103 |
| `sim-logistics` | otel-lgtm | 4318 |
| Job `kafka-topics` | Kafka | 9092 |
| Keycloak | PostgreSQL `pg` (его база в общем кластере) | 5432 |
| поды `pg` | друг другу (репликация, статус); MinIO; API Kubernetes | любые; 9000; 443, 6443 |
| оператор `cnpg-system` | поды `pg` | 8000, 9187, 5432 |
| `pg`, Job `minio-setup` | MinIO | 9000 |

`bs-observability` только принимает (исходящих, кроме DNS, нет).

**K1.** Поды с меткой `blood-supply.io/data-class=k1` (`matching`, `inventory`) исключены из общих правил
сервисов (политики складываются, поэтому общие правила выбирают поды без этой метки) и получают свои
(`app-k1.yaml`): вход в `inventory` — только от `transfer` и `web`, в `matching` — только от `web`; выход —
только Kafka, PostgreSQL, Keycloak, телеметрия и для `inventory` — АИСТ-М. В частности, K1-поды не ходят
во внутренние API других сервисов.

## prod-example

Не разворачивается: собирается и проверяется (`kubectl kustomize`, `tools/k8scheck`, в CI — kubeconform).
Отличия от `local`:

- нет `demo-secrets` и `sims`: секреты — заглушки `secrets-placeholder.yaml` (те же имена и ключи, значение
  `REPLACE_FROM_SECRET_MANAGER`, аннотация `blood-supply.io/secret-source: external`) под внешний менеджер
  секретов; ExternalSecret и подобные CRD не добавлены;
- домен `blood-supply.example.org` в Ingress, `KC_HOSTNAME`, `BS_OIDC_ISSUER`, `BS_WEB_PUBLIC_URL`;
  HSTS включён, демо-доступа нет (значения base);
- Keycloak без `--import-realm`: демо-realm с пользователями `demo` в прод не попадает;
- смежные системы — внешние адреса (`external-systems`) и выход к ним по `ipBlock` (`external-egress.yaml`,
  адреса-заглушки из RFC 5737);
- ИНТ-02 — только с адреса шлюза C02;
- по две реплики у сервисов без HPA.

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
11. **Пробы**: у Kafka и otel-lgtm — TCP и HTTP-проверки порта; пробы сервисов задаёт `components/go-service`.
12. Ни один образ и ни один CRD при подготовке манифестов не скачивались; теги образов `minio/minio`,
    `ghcr.io/cloudnative-pg/postgresql:16.9` и `apache/kafka:3.9.1` проверяются при первой загрузке.
13. **Переменные `demand`, `planning`, `matching` (кроме `BS_K1_KEYS`) и SMTP у `notification`** — уточнить
    при реализации: кода с ними ещё нет. Если сервис выберет другое имя, правится манифест, а не код.
    Сервисы, которых ещё нет в `services/`, не соберутся в образ — их Deployment будет ждать образа
    (`ErrImagePull`), остальное работает. `sim-emergency` пока не получает служебный токен (client credentials
    в имитаторе не подключены): отправка прогноза в `planning` будет отклонена до их подключения.
14. **Тег `edge`** подвижный (`imagePullPolicy: IfNotPresent`); для воспроизводимого стенда автоматизации
    стоит подменять его на собранный тег (`kustomize edit set image`, `kind load docker-image`).
15. **Имитаторы**: состояние в памяти, одна реплика, без PDB; `/dev/*` маршруты открыты внутри кластера —
    закрывает NetworkPolicy и ingress (наружу не публикуются).
