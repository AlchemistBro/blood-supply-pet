#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
# Builds/deploys nothing locally unless explicitly invoked on the disposable
# stand. The Python preflight is also used before building/loading the helper.
python3 tools/chaos/run.py --preflight "$@"
if [[ "${BS_CHAOS_BUILD_INJECTOR:-1}" == 1 ]]; then
  docker build -f tools/chaos/Dockerfile -t blood-supply-chaos:local .
  kind load docker-image --name blood-supply blood-supply-chaos:local
fi
python3 tools/chaos/run.py "$@"
