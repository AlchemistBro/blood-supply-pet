#!/usr/bin/env bash
# Предзагрузка образов стенда и тестов. Запускать ВНЕ рабочей сессии, когда Docker Desktop уже работает
# и VPN стабилен: образы тяжёлые (несколько гигабайт), а обрыв связи посреди слоя - обычное дело.
# Качает по одному, повторяет неудачные попытки; уже скачанные слои Docker не качает заново.
#
#   bash scripts/pull-images.sh
set -uo pipefail

IMAGES=(
  "postgres:16-alpine"
  "apache/kafka-native:3.9.1"
  "confluentinc/confluent-local:7.7.1"
  "axllent/mailpit:v1.22"
  "nginx:1.27-alpine"
  "alpine/openssl:latest"
  "quay.io/keycloak/keycloak:26.4"
  "grafana/otel-lgtm:0.11.0"
  "gcr.io/distroless/static-debian12:nonroot"
  "golang:1.27"
  "testcontainers/ryuk:0.14.0"
)
ATTEMPTS=${ATTEMPTS:-8}
PAUSE=${PAUSE:-20}

if ! docker info >/dev/null 2>&1; then
  echo "Docker не отвечает. Запустите Docker Desktop, дождитесь статуса Running и повторите." >&2
  exit 1
fi

failed=()
for image in "${IMAGES[@]}"; do
  if docker image inspect "$image" >/dev/null 2>&1; then
    echo "[есть]   $image"
    continue
  fi
  ok=0
  for attempt in $(seq 1 "$ATTEMPTS"); do
    echo "[качаю]  $image (попытка $attempt из $ATTEMPTS)"
    if docker pull "$image"; then ok=1; break; fi
    echo "         обрыв, пауза ${PAUSE} с"
    sleep "$PAUSE"
    docker info >/dev/null 2>&1 || { echo "Docker остановился. Запустите его и повторите скрипт." >&2; exit 1; }
  done
  [ "$ok" -eq 1 ] || failed+=("$image")
done

if [ "${#failed[@]}" -gt 0 ]; then
  echo
  echo "Не удалось скачать:" >&2
  printf '  %s\n' "${failed[@]}" >&2
  echo "Повторите скрипт позже: готовые образы он пропустит." >&2
  exit 1
fi
echo
echo "Все образы на месте. Можно запускать: task up"
