# Проверка образов приложения, 26.10.2026

Проверены 12 локально собранных Linux/amd64-образов: `audit`, `inventory`,
`logistics-gateway`, `transfer`, `web`, `demand`, `planning`, `matching`,
`notification`, `sim-aist`, `sim-emergency`, `sim-logistics`.

Прикладные бинарники собраны на Go 1.27.3 из `55546cb` с `CGO_ENABLED=0`,
`-trimpath`, `-s -w`. Итоговый слой соответствует `deploy/docker/Dockerfile`:
`gcr.io/distroless/static-debian12:nonroot`, бинарники `/app` и `/healthcheck`,
пользователь `65532:65532`. Сборка бинарников выполнена на хосте, затем они
помещены в итоговый образ без загрузки модулей при сборке Docker.

Trivy 0.76.1, база уязвимостей обновлена 26.10.2026. Команда для каждого образа:

```bash
trivy image --image-src docker --scanners vuln --severity HIGH,CRITICAL \
  --exit-code 1 --format json --output "$service.json" \
  "ghcr.io/alchemistbro/blood-supply-$service:dev"
```

Результат: **0 HIGH/CRITICAL во всех 12 образах**, код завершения каждого
сканирования — 0. Проверены пакеты ОС и Go-бинарников; находки без доступного
исправления также учитывались. Исключения CVE не добавлялись.

После изменения списка заявок web и matching пересобраны из `511f569`
и повторно проверены той же командой: 0 HIGH/CRITICAL в обоих образах.

В CI после `task images` добавлено такое же сканирование всех 12 образов.
JSON-отчёты сохраняются в артефакт `trivy-images`, включая случай ошибки.
Публикация в реестр для проверки не требуется.

Полный [CI `e1c0ca7`](https://github.com/AlchemistBro/blood-supply-pet/actions/runs/40243649650)
успешен. Все 12 образов собраны исходным `deploy/docker/Dockerfile` на GitHub;
артефакт [trivy-images](https://github.com/AlchemistBro/blood-supply-pet/actions/runs/40243649650/artifacts/12618390614)
содержит 12 JSON-отчётов, в каждом 0 HIGH/CRITICAL. ОС итоговых образов — Debian 12.15.

Результат относится к указанным образам и состоянию базы сканера на дату
проверки. Образы сторонней платформы (Kafka, Keycloak, PostgreSQL, LGTM,
MinIO, ingress и операторы Kubernetes) этим прогоном не проверены.
