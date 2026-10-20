# Этап 8. Kubernetes

Цель: вся система разворачивается в kind одной командой с продовыми практиками; NFR005–NFR007, NFR012 подтверждаются учением восстановления.
Спецификация: `docs/architecture/deployment.md`, ADR-0009.

## Чеклист

### Манифесты
- [ ] `deploy/k8s/base`: namespaces `bs-edge`, `bs-app`, `bs-data`, `bs-observability`; CloudNativePG operator + `Cluster` (2 экземпляра, backup в MinIO, `ScheduledBackup` суточный, retention 7d), `Database` и роль на сервис; Kafka StatefulSet (KRaft) + init топиков; Keycloak + realm ConfigMap; OTel Collector + lgtm; Mailpit; MinIO.
- [ ] На каждый сервис: Deployment (probes, resources, securityContext, env из ConfigMap/Secret), Service, HPA (web, inventory, matching), PDB, NetworkPolicy, ServiceMonitor/PodMonitor (или OTel scrape).
- [ ] ingress-nginx: Ingress для `web` (TLS, самоподписанный в local), путь ИНТ-02 к `planning`, rate limit; `/auth` к Keycloak.
- [ ] NetworkPolicy по сегментам: `bs-data` ← только `bs-app`; `matching` ↔ своя БД отдельной политикой; `bs-observability` только ingress; имитаторы в `bs-sims` (только local/ci).
- [ ] Overlays: `local` (NodePort/ingress на localhost, демо-секреты, имитаторы), `ci` (минимальные ресурсы, Job smoke), `prod-example` (реплики, внешние секреты, без имитаторов; не разворачивается, проверяется kubeconform).
- [ ] `kubeconform` и `kube-linter` в CI на `kustomize build` всех overlays.

### Автоматизация
- [ ] `task k8s:up`: kind create (конфиг с портами 80/443), установка операторов (CNPG, ingress-nginx), `kubectl apply -k overlays/local`, ожидание readiness, вывод URL и демо-учёток.
- [ ] `task k8s:down`, `task k8s:smoke` (e2e против ingress), `task k8s:logs`.
- [ ] `task seed` работает против kind.
- [ ] `.github/workflows/k8s.yml`: kind в Actions → overlay `ci` → smoke Job; nightly → хаос (`tests/chaos`: kill pod `transfer` во время подтверждений, kill `logistics-gateway` во время заказа, пауза Kafka 60 с, недоступность `sim-aist`) с проверкой инвариантов (нет двойного закрепления, нет второго заказа, система догоняет).

### Учение восстановления (NFR006, NFR007, NFR012)
- [ ] `deploy/k8s/drill/drill.sh` (`task k8s:drill`): записать маркер → подождать → «авария» (удалить primary) → восстановление из WAL на `targetTime` → smoke → измерить RTO и потерю данных; отчёт в `docs/perf/drill-<date>.md`.
- [ ] Nightly ускоренный drill в CI; ежемесячный полный — в календаре README.

### Эксплуатационная документация
- [ ] `docs/architecture/deployment.md` обновлён фактическими манифестами; схема развёртывания (deployment diagram) в Mermaid.
- [ ] Runbook `docs/runbooks/`: «C03 молчит», «АИСТ-М недоступна», «DLT не пуст», «relay отстаёт», «восстановление БД».
- [ ] Дашборды Grafana (JSON в репозитории): RED по сервисам, lag outbox/consumer, статусы O07, время расчёта плана.

## Критерий выхода
`task k8s:up` на чистой машине с Docker → через ≤ 10 мин система доступна по `https://localhost`, e2e smoke зелёный;
`task k8s:drill` показывает RTO ≤ 60 мин (в демо минуты) и RPO ≤ 15 мин; k8s.yml зелёный в CI.
