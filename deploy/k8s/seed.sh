#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
k=(kubectl --context kind-blood-supply)
temporary=$(mktemp)
pids=()
cleanup() { for process in "${pids[@]}"; do kill "$process" 2>/dev/null || true; done; rm -f "$temporary"; }
trap cleanup EXIT
forward() { "${k[@]}" -n "$1" port-forward --address 127.0.0.1 "svc/$2" "$3:$4" >> "$temporary" 2>&1 & pids+=("$!"); }
forward bs-app inventory 18101 8101
forward bs-app demand 18102 8102
forward bs-app planning 18103 8103
forward bs-app matching 18104 8104
forward bs-edge keycloak 18180 8080
forward bs-sims sim-aist 18201 8201
for port in 18101 18102 18103 18104 18201; do
  ready=false
  for attempt in $(seq 1 30); do
    if curl -fsS --connect-timeout 1 --max-time 2 "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then ready=true; break; fi
    sleep 1
  done
  [[ "$ready" == true ]] || { cat "$temporary" >&2; exit 1; }
done
BS_SEED_INVENTORY=http://127.0.0.1:18101 BS_SEED_DEMAND=http://127.0.0.1:18102 \
BS_SEED_PLANNING=http://127.0.0.1:18103 BS_SEED_MATCHING=http://127.0.0.1:18104 \
BS_SEED_KEYCLOAK=http://127.0.0.1:18180/auth BS_SEED_AIST=http://127.0.0.1:18201 \
go run ./tools/seed
