# Этап 8. Kubernetes

Цель: вся система разворачивается в kind одной командой с продовыми практиками; NFR005–NFR007, NFR012 подтверждаются учением восстановления.
Спецификация: `docs/architecture/deployment.md`, ADR-0009.

## Чеклист

Состояние 26.10.2026: [Kubernetes workflow `e1c0ca7`](https://github.com/AlchemistBro/blood-supply-pet/actions/runs/40243693985)
прошёл в kind на GitHub с overlay `ci`: развёртывание платформы и сервисов,
разрешённый/запрещённый сетевой поток, TLS и HTTP/роль smoke, отказ primary и PITR.
RTO до сверки маркеров и готовности исходного кластера — 218 с, интервал до цели — 4 с,
маркеры — 1/0. Это управляемое учение, не общая гарантия RPO.
[Отчёт, артефакты и ограничения](../perf/pitr-2026-10-26.md).
Холодная сборка с развёртыванием заняла 16 мин 58 с на двух CPU:
критерий ≤ 10 мин пока не подтверждён. Образы собраны на runner без публикации.

### Манифесты
- [x] `deploy/k8s/base`: namespaces `bs-edge`, `bs-app`, `bs-data`, `bs-observability`; CloudNativePG operator + `Cluster` (2 экземпляра, backup в MinIO, `ScheduledBackup` суточный, retention 7d), `Database` и роль на сервис; Kafka StatefulSet (KRaft) + init топиков; Keycloak + realm ConfigMap; OTel Collector + lgtm; Mailpit; MinIO. Развёрнуто и проверено в `ci`.
- [ ] На каждый сервис: Deployment (probes, resources, securityContext, env из ConfigMap/Secret), Service, HPA (web, inventory, matching), PDB, NetworkPolicy, ServiceMonitor/PodMonitor (или OTel scrape).
- [x] ingress-nginx: Ingress для `web` (TLS, самоподписанный в local), путь ИНТ-02 к `planning`, rate limit; `/auth` к Keycloak. Exact-маршруты `/auth/login` и `/auth/callback` ведут в BFF; TLS и разделение маршрутов проверены smoke.
- [ ] NetworkPolicy по сегментам: `bs-data` ← только `bs-app`; `matching` ↔ своя БД отдельной политикой; `bs-observability` только ingress; имитаторы в `bs-sims` (только local/ci).
- [x] Overlays: `local` (NodePort/ingress на localhost, демо-секреты, имитаторы), `ci` (минимальные ресурсы, smoke с runner), `prod-example` (реплики, внешние секреты, без имитаторов). Все три собираются kustomize и проверяются Trivy; фактически развёрнут только `ci`.
- [ ] `kubeconform` и `kube-linter` в CI на `kustomize build` всех overlays.

### Автоматизация
- [x] `task k8s:up`: kind create (конфиг с портами 80/443), установка операторов (CNPG, ingress-nginx), применение выбранного overlay, ожидание платформы и сервисов, вывод URL и демо-учёток; подтверждено прогоном `ci`.
- [ ] `task k8s:down`, `task k8s:smoke` (e2e против ingress), `task k8s:logs`.
- [ ] `task seed` работает против kind.
- [ ] Kubernetes chaos/nightly: текущий workflow проверяет overlay `ci`, smoke и PITR (вручную с `drill=true` и еженедельно по расписанию). Сага/пауза Kafka проверены отдельным compose E2E; Kubernetes chaos из исходного плана остаётся открытым.

### Учение восстановления (NFR006, NFR007, NFR012)
- [x] `deploy/k8s/drill/drill.sh` (`task k8s:drill`): маркер → завершённая копия → цель PITR → второй маркер → проверка WAL → удаление primary → восстановление на `targetTime` → сверка 1/0 и готовности исходного кластера; RTO 218 с, окно до цели 4 с. Исходный отчёт `drill-20261025233122.md` в артефакте, [публичная сводка](../perf/pitr-2026-10-26.md). Полный прикладной smoke восстановленной копии — отдельная открытая проверка.
- [ ] Nightly и ежемесячный drill из исходного плана: сейчас настроен еженедельный запуск; подтверждён ручной прогон.

### Эксплуатационная документация
- [ ] `docs/architecture/deployment.md` обновлён фактическими манифестами; схема развёртывания (deployment diagram) в Mermaid.
- [ ] Runbook `docs/runbooks/`: «C03 молчит», «АИСТ-М недоступна», «DLT не пуст», «relay отстаёт», «восстановление БД».
- [ ] Дашборды Grafana (JSON в репозитории): RED по сервисам, lag outbox/consumer, статусы O07, время расчёта плана.

## Критерий выхода
`task k8s:up` на чистой машине с Docker → через ≤ 10 мин система доступна по `https://localhost`, e2e smoke зелёный;
`task k8s:drill` показывает RTO ≤ 60 мин (в демо минуты) и RPO ≤ 15 мин; k8s.yml зелёный в CI.
