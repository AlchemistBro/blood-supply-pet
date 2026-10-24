#!/usr/bin/env bash
# Сквозной прогон саги на поднятом стенде (task up:saga) для ручного показа — те же пять сценариев, что
# tests/e2e/saga_test.go: заказ, выбытие → BROKEN → снятие заказа, потерянный ответ C03, шлюз остановлен,
# kill -9 шлюза посреди заказа. Нужны bash, curl, jq и docker compose. Останавливает только контейнер
# logistics-gateway стенда.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
COMPOSE=(docker compose -f "$ROOT/deploy/compose/compose.yaml" --profile services)
[ -f "$ROOT/deploy/compose/.env" ] && COMPOSE+=(--env-file "$ROOT/deploy/compose/.env")

KEYCLOAK=${KEYCLOAK:-http://localhost:8180}
TRANSFER=${TRANSFER:-http://localhost:8105}
GATEWAY=${GATEWAY:-http://localhost:8106}
AIST=${AIST:-http://localhost:8201}
C03=${C03:-http://localhost:8203}
TOKEN_URL="$KEYCLOAK/realms/blood-supply/protocol/openid-connect/token"

uuid() { cat /proc/sys/kernel/random/uuid 2>/dev/null || uuidgen | tr 'A-Z' 'a-z'; }
post() { curl -sf -X POST -H 'Content-Type: application/json' "${@:3}" -d "$2" "$1"; }

service_token() {
  curl -sf -d grant_type=client_credentials -d client_id=services \
    -d "client_secret=${SERVICES_CLIENT_SECRET:-services-demo-secret}" "$TOKEN_URL" | jq -r .access_token
}
operator_token() {
  curl -sf -d grant_type=password -d client_id=blood-supply-cli -d username=operator-c001 -d password=demo \
    "$TOKEN_URL" | jq -r .access_token
}

# wait_for <описание> <команда...>: повторяет команду, пока она не выведет "ok" (до 2 минут).
wait_for() {
  local what=$1; shift
  for _ in $(seq 1 240); do
    [ "$("$@" 2>/dev/null || true)" = ok ] && return 0
    sleep 0.5
  done
  echo "не дождались: $what" >&2
  exit 1
}

sql() { # база запрос — только чтение, суперпользователем стенда
  "${COMPOSE[@]}" exec -T postgres psql -U postgres -d "$1" -tAc "$2"
}

status_of() { curl -sf -H "Authorization: Bearer $(service_token)" "$TRANSFER/obligations/$1" | jq -r .status; }
is_status() { [ "$(status_of "$1")" = "$2" ] && echo ok; }
shipments() { curl -sf "$C03/dev/shipments?clientRef=o07-$1" | jq -c .shipments; }

# new_unit: единица у C001 в АИСТ-М → копия inventory → копия transfer; печатает "<aist_code> <unit_id>".
new_unit() {
  local code id
  code="SMOKE-$(uuid | tr -d - | cut -c1-16)"
  post "$AIST/dev/units" "{\"aist_unit_code\":\"$code\",\"clinic_code\":\"C001\",\"component_kind\":\"RBC\",\
\"blood_group\":\"O+\",\"storage_location\":\"smoke\"}" >/dev/null
  wait_for "единица $code в inventory" sh -c "[ -n \"\$(${COMPOSE[*]} exec -T postgres psql -U postgres -d inventory -tAc \
\"SELECT unit_id FROM unit WHERE aist_unit_code = '$code'\")\" ] && echo ok"
  id=$(sql inventory "SELECT unit_id FROM unit WHERE aist_unit_code = '$code'")
  wait_for "единица $id в transfer" sh -c "[ \"\$(${COMPOSE[*]} exec -T postgres psql -U postgres -d transfer -tAc \
\"SELECT unit_status_code FROM unit_copy WHERE unit_id = '$id'\")\" = IN_STOCK ] && echo ok"
  echo "$code $id"
}

open_obligation() { # количество
  post "$TRANSFER/obligations" "{\"choice_id\":\"$(uuid)\",\"request_id\":\"$(uuid)\",\"origin\":\"DOCTOR_CHOICE\",\
\"source_clinic_id\":1,\"target_clinic_id\":2,\"component_kind\":\"RBC\",\"requested_qty\":$1,\"urgency\":\"PLANNED\",\
\"confirm_window_seconds\":3600,\"spec\":{\"rule_version\":\"smoke-demo\"}}" \
    -H "Authorization: Bearer $(service_token)" | jq -r .obligation_id
}

confirm() { # obligation_id unit_id...
  local id=$1; shift
  local units
  units=$(printf '"%s",' "$@"); units="[${units%,}]"
  post "$TRANSFER/obligations/$id/confirm" "{\"unit_ids\":$units,\"idempotency_key\":\"$(uuid)\"}" \
    -H "Authorization: Bearer $(operator_token)" | jq -r .code
}

faults() { post "$C03/dev/faults" "$1" >/dev/null; }
trap 'faults "{\"failNextCalls\":0,\"loseNextSubmitResponse\":false,\"refuseNext\":false,\"slowMs\":0}" || true;
      "${COMPOSE[@]}" start logistics-gateway >/dev/null 2>&1 || true' EXIT

echo "1. Подтверждение двух единиц → TRANSPORT_REQUESTED и заказ у C03"
read -r A1 U1 <<<"$(new_unit)"; read -r _ U2 <<<"$(new_unit)"
O1=$(open_obligation 2)
echo "   подтверждение: $(confirm "$O1" "$U1" "$U2")"
wait_for "O07 $O1 TRANSPORT_REQUESTED" is_status "$O1" TRANSPORT_REQUESTED
echo "   O07 $O1: TRANSPORT_REQUESTED, заказы у C03: $(shipments "$O1")"

echo "2. Единица выдана в АИСТ-М → O07 BROKEN → заказ у C03 снят"
post "$AIST/dev/units/$A1/withdraw" '{"reason":"ISSUED"}' >/dev/null
wait_for "O07 $O1 BROKEN" is_status "$O1" BROKEN
wait_for "заказ снят" sh -c "curl -sf '$C03/dev/shipments?clientRef=o07-$O1' | jq -e '.shipments | length == 1 and .[0].cancelled' >/dev/null && echo ok"
echo "   заказ у C03 снят: $(shipments "$O1")"

echo "3. C03 создаёт заказ, но ответ теряется → сверка, второго заказа нет"
read -r _ U3 <<<"$(new_unit)"
O2=$(open_obligation 1)
faults '{"loseNextSubmitResponse":true}'
echo "   подтверждение: $(confirm "$O2" "$U3")"
wait_for "O07 $O2 TRANSPORT_REQUESTED" is_status "$O2" TRANSPORT_REQUESTED
COUNT=$(curl -sf "$C03/dev/shipments?clientRef=o07-$O2" | jq '.shipments | length')
[ "$COUNT" = 1 ] || { echo "у C03 $COUNT заказов вместо одного" >&2; exit 1; }
echo "   O07 $O2: у C03 ровно один заказ"

echo "4. Шлюз остановлен: подтверждение проходит, заказ уходит после старта"
read -r _ U4 <<<"$(new_unit)"
O3=$(open_obligation 1)
"${COMPOSE[@]}" stop logistics-gateway >/dev/null 2>&1
echo "   подтверждение при остановленном шлюзе: $(confirm "$O3" "$U4"), статус $(status_of "$O3")"
"${COMPOSE[@]}" start logistics-gateway >/dev/null 2>&1
wait_for "O07 $O3 TRANSPORT_REQUESTED" is_status "$O3" TRANSPORT_REQUESTED
echo "   шлюз дочитал команду из Kafka: TRANSPORT_REQUESTED, заказы у C03: $(shipments "$O3")"

echo "5. kill -9 шлюза посреди заказа → после старта ровно один заказ"
read -r _ U5 <<<"$(new_unit)"
O4=$(open_obligation 1)
faults '{"slowMs":10000}'
echo "   подтверждение: $(confirm "$O4" "$U5")"
wait_for "шлюз взял аренду" sh -c "[ \"\$(${COMPOSE[*]} exec -T postgres psql -U postgres -d logistics -tAc \
\"SELECT status FROM transport_request WHERE obligation_id = '$O4' AND lease_owner IS NOT NULL\")\" = REQUESTING ] && echo ok"
"${COMPOSE[@]}" kill -s KILL logistics-gateway >/dev/null 2>&1
faults '{"slowMs":0}'
"${COMPOSE[@]}" start logistics-gateway >/dev/null 2>&1
wait_for "шлюз жив" sh -c "curl -sf '$GATEWAY/healthz' >/dev/null && echo ok"
wait_for "O07 $O4 TRANSPORT_REQUESTED (аренда убитого шлюза истекает за 30 с)" is_status "$O4" TRANSPORT_REQUESTED
COUNT=$(curl -sf "$C03/dev/shipments?clientRef=o07-$O4" | jq '.shipments | length')
[ "$COUNT" = 1 ] || { echo "после kill -9 у C03 $COUNT заказов вместо одного" >&2; exit 1; }
echo "   O07 $O4: у C03 ровно один заказ"

echo "Сага прошла."
