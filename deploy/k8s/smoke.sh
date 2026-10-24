#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
k=(kubectl --context kind-blood-supply)
"${k[@]}" -n bs-app delete job bs-smoke --ignore-not-found >/dev/null
"${k[@]}" -n bs-app create configmap bs-smoke --from-file=check.sh=deploy/k8s/smoke-check.sh --dry-run=client -o yaml | "${k[@]}" apply -f -
"${k[@]}" -n bs-app apply -f - <<'YAML'
apiVersion: batch/v1
kind: Job
metadata: {name: bs-smoke}
spec:
  backoffLimit: 0
  activeDeadlineSeconds: 240
  ttlSecondsAfterFinished: 3600
  template:
    metadata:
      labels: {blood-supply.io/runtime: go-service, app.kubernetes.io/name: bs-smoke}
    spec:
      restartPolicy: Never
      containers:
        - name: check
          image: curlimages/curl:8.12.1
          command: [sh, /checks/check.sh]
          volumeMounts: [{name: checks, mountPath: /checks, readOnly: true}]
          resources: {requests: {cpu: 10m, memory: 32Mi}, limits: {memory: 64Mi}}
          securityContext: {runAsNonRoot: true, allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [ALL]}}
      volumes: [{name: checks, configMap: {name: bs-smoke}}]
YAML
if ! "${k[@]}" -n bs-app wait --for=condition=complete job/bs-smoke --timeout=260s; then
  "${k[@]}" -n bs-app logs job/bs-smoke || true
  exit 1
fi
"${k[@]}" -n bs-app logs job/bs-smoke
