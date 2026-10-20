# ADR-0009. Kubernetes как целевая среда: kind, kustomize, CloudNativePG

Статус: принят, 20.10.2026.

## Контекст

Владелец хочет архитектуру, «которую потом можно выложить в реальный продакшн», с Kubernetes. ПР6 предлагает Patroni и
pgBackRest для NFR005–NFR007 и NFR012. Нужна локальная воспроизводимость на ноутбуке с Windows.

## Решение

- Локально и в CI — `kind`; манифесты — kustomize (`base` + overlays `local`, `ci`, `prod-example`). Helm не используется.
- PostgreSQL — оператор CloudNativePG: 2 экземпляра, непрерывный архив WAL в MinIO, PITR, `ScheduledBackup`; это заменяет
  Patroni + pgBackRest из ПР6 теми же гарантиями (реплика, RPO ≤ 15 мин, RTO ≤ 60 мин).
- Kafka — один брокер StatefulSet (без Strimzi) для простоты; в `prod-example` комментарий о Strimzi.
- Keycloak, OTel Collector, `grafana/otel-lgtm`, Mailpit — Deployment.
- Сегменты ПР4 отражаются namespace-ами и NetworkPolicy; ingress-nginx — единственный вход.
- Стандарт сервиса: probes, resources, non-root, read-only FS, PDB, HPA для stateless.
- Compose остаётся для разработки и быстрого демо; k8s — для «продовой» демонстрации и учения восстановления.

## Последствия

Этап 8 плана; CI с kind добавляет минуты к пайплайну (nightly для хаоса и drill); образы должны быть минимальными.

## Условия пересмотра

Переход на управляемый Kubernetes и внешний Secret-менеджер при реальном развёртывании (`prod-example` описывает).
