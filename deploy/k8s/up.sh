#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
started=$(date +%s)
mode=source
[[ -z ${BS_K8S_RELEASE_MANIFEST:-} ]] || mode=release
overlay=${BS_K8S_OVERLAY:-local}
[[ "$overlay" == local || "$overlay" == ci ]] || { printf '%s\n' 'Допустимы только local и ci.' >&2; exit 2; }
for command in kind kubectl docker go openssl python3; do command -v "$command" >/dev/null || { printf 'Не найдена команда %s\n' "$command" >&2; exit 2; }; done
temporary=$(mktemp -d)
cleanup() {
  result=$?
  elapsed=$(( $(date +%s) - started ))
  mkdir -p dist
  python3 - "$started" "$elapsed" "$mode" "$result" <<'PY'
import json, pathlib, sys
pathlib.Path('dist/k8s-startup.json').write_text(json.dumps({
    'started_epoch': int(sys.argv[1]), 'elapsed_seconds': int(sys.argv[2]),
    'mode': sys.argv[3], 'exit_code': int(sys.argv[4]),
    'limit_seconds': 600 if sys.argv[3] == 'release' else None}, indent=2))
PY
  rm -f "$temporary/overlay.yaml" "$temporary/source.yaml" "$temporary/tls.crt" "$temporary/tls.key"
  rmdir "$temporary"
}
trap cleanup EXIT
kubectl kustomize "deploy/k8s/overlays/$overlay" > "$temporary/source.yaml"
if [[ "$mode" == release ]]; then
  go run ./tools/k8simages -manifest "$BS_K8S_RELEASE_MANIFEST" -commit "$(git rev-parse HEAD)" \
    -repository "${GITHUB_REPOSITORY:-AlchemistBro/blood-supply-pet}" < "$temporary/source.yaml" > "$temporary/overlay.yaml"
else
  cp "$temporary/source.yaml" "$temporary/overlay.yaml"
fi
go run ./tools/k8scheck "$temporary/overlay.yaml"
if ! kind get clusters | grep -qx blood-supply; then
  kind create cluster --name blood-supply --config deploy/k8s/kind.yaml
fi
k=(kubectl --context kind-blood-supply)
"${k[@]}" apply --server-side -f https://raw.githubusercontent.com/projectcalico/calico/v3.30.3/manifests/calico.yaml
"${k[@]}" rollout status -n kube-system daemonset/calico-node --timeout=300s
"${k[@]}" wait --for=condition=Ready nodes --all --timeout=300s
if [[ "$mode" == source && ${BS_K8S_BUILD_IMAGES:-1} == 1 ]]; then
  for service in audit inventory logistics-gateway transfer web demand planning matching notification sim-aist sim-emergency sim-logistics; do
    image="ghcr.io/alchemistbro/blood-supply-$service:edge"
    docker build -f deploy/docker/Dockerfile --build-arg "SERVICE=$service" --build-arg VERSION=kind -t "$image" .
    kind load docker-image --name blood-supply "$image"
  done
  image=ghcr.io/alchemistbro/blood-supply-minio:source-2025-10-15
  docker build -f deploy/docker/Minio.Dockerfile -t "$image" .
  kind load docker-image --name blood-supply "$image"
fi
"${k[@]}" apply --server-side -f https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.27/releases/cnpg-1.27.0.yaml
"${k[@]}" rollout status -n cnpg-system deployment/cnpg-controller-manager --timeout=180s
"${k[@]}" apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.11.3/deploy/static/provider/kind/deploy.yaml
"${k[@]}" wait -n ingress-nginx --for=condition=Ready pod -l app.kubernetes.io/component=controller --timeout=180s
# kind uses test kubelet certificates; this flag is restricted to the local/ci
# stand guarded above. Production metrics-server must validate serving certificates.
"${k[@]}" apply --server-side -f https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.8.1/components.yaml
"${k[@]}" -n kube-system patch deployment metrics-server --type=json -p \
  '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
bash deploy/k8s/network-check.sh
if [[ "$mode" == release && -n ${BS_K8S_REGISTRY_CONFIG:-} ]]; then
  for namespace in bs-app bs-data bs-sims; do
    "${k[@]}" create namespace "$namespace" --dry-run=client -o yaml | "${k[@]}" apply -f -
    "${k[@]}" -n "$namespace" create secret generic ghcr-pull \
      --type=kubernetes.io/dockerconfigjson --from-file=".dockerconfigjson=$BS_K8S_REGISTRY_CONFIG" \
      --dry-run=client -o yaml | "${k[@]}" apply -f -
    "${k[@]}" -n "$namespace" create serviceaccount default --dry-run=client -o yaml | "${k[@]}" apply -f -
    "${k[@]}" -n "$namespace" patch serviceaccount default --type=merge -p '{"imagePullSecrets":[{"name":"ghcr-pull"}]}'
  done
fi
go run ./tools/k8scheck "$temporary/overlay.yaml"
go run ./tools/k8sphase -phase foundation < "$temporary/overlay.yaml" | "${k[@]}" apply --server-side -f -
MSYS_NO_PATHCONV=1 openssl req -x509 -nodes -newkey rsa:2048 -days 30 -subj /CN=localhost -addext subjectAltName=DNS:localhost,IP:127.0.0.1 -keyout "$temporary/tls.key" -out "$temporary/tls.crt"
for namespace in bs-app bs-edge; do
  "${k[@]}" -n "$namespace" create secret tls bs-tls --cert "$temporary/tls.crt" --key "$temporary/tls.key" --dry-run=client -o yaml | "${k[@]}" apply -f -
done
"${k[@]}" wait -n bs-data --for=condition=complete job/minio-setup --timeout=300s
"${k[@]}" wait -n bs-data --for=condition=Ready cluster/pg --timeout=600s
"${k[@]}" wait -n bs-app --for=condition=complete job/kafka-topics --timeout=300s
"${k[@]}" rollout status -n bs-edge deployment/keycloak --timeout=600s
"${k[@]}" rollout status -n bs-observability deployment/otel-lgtm --timeout=600s
"${k[@]}" wait -n bs-sims --for=condition=Available deployment --all --timeout=300s
go run ./tools/k8sphase -phase application < "$temporary/overlay.yaml" | "${k[@]}" apply --server-side -f -
"${k[@]}" wait -n bs-app --for=condition=Available deployment --all --timeout=600s
"${k[@]}" rollout status -n kube-system deployment/metrics-server --timeout=180s
"${k[@]}" -n bs-app wait --for=condition=ScalingActive horizontalpodautoscaler --all --timeout=180s
mkdir -p dist
"${k[@]}" get --raw /apis/metrics.k8s.io/v1beta1/namespaces/bs-app/pods > dist/k8s-pod-metrics.json
"${k[@]}" -n bs-app get hpa -o json > dist/k8s-hpa.json
"${k[@]}" -n bs-app get deployment -l blood-supply.io/runtime=go-service -o json > dist/k8s-readiness.json
elapsed=$(( $(date +%s) - started ))
printf 'Полный запуск kind (%s): %s с.\n' "$mode" "$elapsed"
if [[ "$mode" == release ]] && (( elapsed > 600 )); then
  printf '%s\n' 'Превышен предел 600 с для развёртывания готового релиза.' >&2
  exit 1
fi
printf '%s\n' 'Стенд: https://localhost; Keycloak: https://localhost/auth; демо-пароль: demo.'
printf '%s\n' 'Для проверки выполните task k8s:smoke. Самоподписанный TLS предназначен для локального демо.'
