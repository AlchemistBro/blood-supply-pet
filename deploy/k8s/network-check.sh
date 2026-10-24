#!/usr/bin/env bash
set -euo pipefail
# Только выделенный kind; проверка не меняет политики приложения.
k=(kubectl --context kind-blood-supply)
namespace=bs-network-check
cleanup() { "${k[@]}" delete namespace "$namespace" --ignore-not-found --wait=false >/dev/null; }
trap cleanup EXIT
"${k[@]}" create namespace "$namespace" --dry-run=client -o yaml | "${k[@]}" apply -f -
"${k[@]}" -n "$namespace" apply -f - <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: server
  labels: {app: network-check-server}
spec:
  containers:
    - name: server
      image: hashicorp/http-echo:1.0.0
      args: ["-listen=:5678", "-text=network-check"]
      ports: [{containerPort: 5678}]
      resources: {requests: {cpu: 10m, memory: 16Mi}, limits: {memory: 32Mi}}
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: restrict-server}
spec:
  podSelector: {matchLabels: {app: network-check-server}}
  policyTypes: [Ingress]
  ingress:
    - from: [{podSelector: {matchLabels: {network-check-allowed: "true"}}}]
      ports: [{protocol: TCP, port: 5678}]
YAML
"${k[@]}" -n "$namespace" wait --for=condition=Ready pod/server --timeout=180s
target=$("${k[@]}" -n "$namespace" get pod server -o jsonpath='{.status.podIP}')
for mode in allowed denied; do
  allowed=false
  command='if curl -fsS --connect-timeout 3 --max-time 5 "$TARGET"; then exit 1; fi'
  if [[ "$mode" == allowed ]]; then
    allowed=true
    command='curl -fsS --connect-timeout 3 --max-time 5 "$TARGET"'
  fi
  "${k[@]}" -n "$namespace" delete job "$mode" --ignore-not-found >/dev/null
  "${k[@]}" -n "$namespace" apply -f - <<YAML
apiVersion: batch/v1
kind: Job
metadata: {name: $mode}
spec:
  backoffLimit: 0
  activeDeadlineSeconds: 120
  template:
    metadata:
      labels: {network-check-allowed: "$allowed"}
    spec:
      restartPolicy: Never
      containers:
        - name: curl
          image: curlimages/curl:8.12.1
          command: [sh, -ec]
          args:
            - '$command'
          env: [{name: TARGET, value: "http://$target:5678"}]
          resources: {requests: {cpu: 10m, memory: 16Mi}, limits: {memory: 32Mi}}
YAML
  if ! "${k[@]}" -n "$namespace" wait --for=condition=complete "job/$mode" --timeout=150s; then
    "${k[@]}" -n "$namespace" logs "job/$mode" || true
    printf '%s\n' 'NetworkPolicy не подтверждена: требуется CNI с исполнением политик.' >&2
    exit 1
  fi
done
printf '%s\n' 'NetworkPolicy: разрешённый поток доступен, чужой источник заблокирован.'
