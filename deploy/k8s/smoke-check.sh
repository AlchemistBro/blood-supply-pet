#!/bin/sh
set -eu
web=${BS_SMOKE_WEB_URL:-https://localhost}
keycloak=${BS_SMOKE_TOKEN_URL:-https://localhost/auth/realms/blood-supply/protocol/openid-connect/token}
matching=${BS_SMOKE_MATCHING_URL:-http://127.0.0.1:18104}
audit=${BS_SMOKE_AUDIT_URL:-http://127.0.0.1:18108}
ca_bundle=${BS_SMOKE_CA_BUNDLE:?Укажите сертификат локального стенда}
curl --cacert "$ca_bundle" -fsS --connect-timeout 3 --max-time 15 "$web/login" >/dev/null
token=$(curl --cacert "$ca_bundle" -fsS --connect-timeout 3 --max-time 15 -X POST "$keycloak" \
  -d grant_type=password -d client_id=blood-supply-cli -d username=physician1 -d password=demo | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
[ -n "$token" ] || { echo 'Keycloak не выдал токен демо-врачу' >&2; exit 1; }
curl --cacert "$ca_bundle" -fsS --connect-timeout 3 --max-time 10 -H "Authorization: Bearer $token" "$matching/requests" >/dev/null
status=$(curl --cacert "$ca_bundle" -sS --connect-timeout 3 --max-time 10 -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $token" "$audit/records")
[ "$status" = 403 ] || { echo "Журнал аудита доступен чужой роли: HTTP $status" >&2; exit 1; }
echo 'Smoke: web доступен через TLS, токен принят matching, врачу отказано в чтении аудита.'
