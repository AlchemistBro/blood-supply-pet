#!/bin/sh
# Создаёт топики из /config/topics.txt, существующие пропускает (--if-not-exists): Job можно
# запускать повторно. Партиции и хранение у существующих топиков не меняются.
set -eu

BOOTSTRAP="${KAFKA_BOOTSTRAP:-kafka.bs-app.svc:9092}"
BIN=/opt/kafka/bin

i=0
until "$BIN/kafka-topics.sh" --bootstrap-server "$BOOTSTRAP" --list >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -ge 60 ]; then
    echo "Kafka $BOOTSTRAP не ответила за 5 минут" >&2
    exit 1
  fi
  sleep 5
done

grep -v '^#' /config/topics.txt | while read -r topic partitions retention; do
  [ -z "$topic" ] && continue
  "$BIN/kafka-topics.sh" --bootstrap-server "$BOOTSTRAP" --create --if-not-exists \
    --topic "$topic" --partitions "$partitions" --replication-factor 1 \
    --config "retention.ms=$retention" --config min.insync.replicas=1
done

echo "топики:"
"$BIN/kafka-topics.sh" --bootstrap-server "$BOOTSTRAP" --list
