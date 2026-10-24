#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
# Деструктивные воздействия ограничены контейнерами отдельного compose-стенда из e2e-обвязки.
go test -tags e2e -count=1 -timeout 20m ./tests/e2e \
  -run '^TestSaga(FiftyConcurrentConfirmationsAllocateOneUnit|KafkaPausePreservesConfirmationAndSingleTransport|LostCarrierResponseGivesExactlyOneShipment|ConfirmationWhileGatewayIsDownOrdersAfterStart|GatewayKilledMidOrderGivesExactlyOneShipment)$'
