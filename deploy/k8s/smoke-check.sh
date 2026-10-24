#!/bin/sh
set -eu
for target in web:8080 inventory:8101 demand:8102 planning:8103 matching:8104 transfer:8105 logistics-gateway:8106 notification:8107 audit:8108; do
  curl -fsS --connect-timeout 3 --max-time 10 "http://$target/readyz" >/dev/null
done
token=$(curl -fsS --connect-timeout 3 --max-time 15 -X POST http://keycloak.bs-edge.svc:8080/auth/realms/blood-supply/protocol/openid-connect/token \
  -d grant_type=password -d client_id=blood-supply-cli -d username=physician1 -d password=demo | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
[ -n "$token" ] || { echo 'Keycloak не выдал токен демо-врачу' >&2; exit 1; }
curl -fsS --connect-timeout 3 --max-time 10 -H "Authorization: Bearer $token" http://matching:8104/requests >/dev/null
status=$(curl -sS --connect-timeout 3 --max-time 10 -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $token" http://audit:8108/records)
[ "$status" = 403 ] || { echo "Журнал аудита доступен чужой роли: HTTP $status" >&2; exit 1; }
echo 'Smoke: девять сервисов готовы, вход работает, врачу отказано в чтении аудита.'
