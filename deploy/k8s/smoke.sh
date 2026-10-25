#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
k=(kubectl --context kind-blood-supply)
# Пробы выполняются внутри существующих подов. Отдельный Job не имеет права
# обращаться к web и данным K1; ради проверки NetworkPolicy не расширяются.
for target in web:8080 inventory:8101 demand:8102 planning:8103 matching:8104 transfer:8105 logistics-gateway:8106 notification:8107 audit:8108; do
  MSYS_NO_PATHCONV=1 "${k[@]}" -n bs-app exec "deployment/${target%:*}" -- /healthcheck "http://127.0.0.1:${target#*:}/readyz"
done

temporary=$(mktemp -d)
matching_pid= audit_pid=
cleanup() {
  for pid in "$matching_pid" "$audit_pid"; do
    if [[ -n "$pid" ]]; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
  done
  rm -f "$temporary/matching.log" "$temporary/audit.log" "$temporary/tls.crt"
  rmdir "$temporary"
}
trap cleanup EXIT
"${k[@]}" -n bs-app get secret bs-tls -o jsonpath='{.data.tls\.crt}' | base64 --decode > "$temporary/tls.crt"

# Port-forward проверяет JWT и прикладные права через API Kubernetes. Исполнение
# NetworkPolicy проверяется отдельно в network-check.sh до развёртывания приложения.
"${k[@]}" -n bs-app port-forward --address=127.0.0.1 service/matching 18104:8104 > "$temporary/matching.log" 2>&1 &
matching_pid=$!
"${k[@]}" -n bs-app port-forward --address=127.0.0.1 service/audit 18108:8108 > "$temporary/audit.log" 2>&1 &
audit_pid=$!
ready=false
for attempt in $(seq 1 60); do
  if curl -fsS --connect-timeout 1 --max-time 2 http://127.0.0.1:18104/readyz >/dev/null 2>&1 &&
     curl -fsS --connect-timeout 1 --max-time 2 http://127.0.0.1:18108/readyz >/dev/null 2>&1; then
    ready=true
    break
  fi
  if ! kill -0 "$matching_pid" "$audit_pid" 2>/dev/null; then break; fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  cat "$temporary/matching.log" "$temporary/audit.log" >&2
  exit 1
fi
BS_SMOKE_CA_BUNDLE="$temporary/tls.crt" bash deploy/k8s/smoke-check.sh
