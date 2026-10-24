#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
overlay=${BS_K8S_OVERLAY:-local}
[[ "$overlay" == local || "$overlay" == ci ]] || { printf '%s\n' 'Допустимы только local и ci.' >&2; exit 2; }
for command in kind kubectl docker go openssl; do command -v "$command" >/dev/null || { printf 'Не найдена команда %s\n' "$command" >&2; exit 2; }; done
if ! kind get clusters | grep -qx blood-supply; then
  kind create cluster --name blood-supply --config deploy/k8s/kind.yaml
fi
k=(kubectl --context kind-blood-supply)
"${k[@]}" apply --server-side -f https://raw.githubusercontent.com/projectcalico/calico/v3.30.3/manifests/calico.yaml
"${k[@]}" rollout status -n kube-system daemonset/calico-node --timeout=300s
"${k[@]}" wait --for=condition=Ready nodes --all --timeout=300s
if [[ ${BS_K8S_BUILD_IMAGES:-1} == 1 ]]; then
  for service in audit inventory logistics-gateway transfer web demand planning matching notification sim-aist sim-emergency sim-logistics; do
    image="ghcr.io/alchemistbro/blood-supply-$service:edge"
    docker build -f deploy/docker/Dockerfile --build-arg "SERVICE=$service" --build-arg VERSION=kind -t "$image" .
    kind load docker-image --name blood-supply "$image"
  done
fi
"${k[@]}" apply --server-side -f https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.27/releases/cnpg-1.27.0.yaml
"${k[@]}" rollout status -n cnpg-system deployment/cnpg-controller-manager --timeout=180s
"${k[@]}" apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.11.3/deploy/static/provider/kind/deploy.yaml
"${k[@]}" wait -n ingress-nginx --for=condition=Ready pod -l app.kubernetes.io/component=controller --timeout=180s
bash deploy/k8s/network-check.sh
temporary=$(mktemp -d)
trap 'rm -f "$temporary/overlay.yaml" "$temporary/tls.crt" "$temporary/tls.key"; rmdir "$temporary"' EXIT
"${k[@]}" kustomize "deploy/k8s/overlays/$overlay" > "$temporary/overlay.yaml"
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
go run ./tools/k8sphase -phase application < "$temporary/overlay.yaml" | "${k[@]}" apply --server-side -f -
"${k[@]}" wait -n bs-app --for=condition=Available deployment --all --timeout=600s
printf '%s\n' 'Стенд: https://localhost; Keycloak: https://localhost/auth; демо-пароль: demo.'
printf '%s\n' 'Для проверки выполните task k8s:smoke. Самоподписанный TLS предназначен для локального демо.'
