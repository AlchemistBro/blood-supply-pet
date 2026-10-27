# Kubernetes failure and alert verification

Run only on the disposable `kind-blood-supply` local/ci stand after application
readiness and before PITR. Nothing here creates a cluster, resets Docker, changes
NetworkPolicy/RBAC, or connects to a cloud database. Do not run this concurrently
with rollout, restore, autoscaling changes, or another chaos run.

Command (Linux GitHub runner, from repository root):

```sh
BS_CHAOS_EPHEMERAL=1 bash deploy/k8s/chaos.sh
```

The wrapper preflights the target, builds `tools/chaos/Dockerfile`, loads
`blood-supply-chaos:local` into the existing kind cluster, then runs all scenarios.
It needs Python 3, kubectl, Docker, kind, and the existing kubeconfig permissions.
No additional service account, Role, or binding is created. To reuse a previously
loaded helper image, set `BS_CHAOS_BUILD_INJECTOR=0`. Build fresh application images
including the gateway metric wiring before starting the stand.

The healthy saga is independently reusable after all service roles/DB URLs and
Keycloak have been moved to the restored cluster:

```sh
BS_CHAOS_EPHEMERAL=1 python3 tools/chaos/run.py \
  --scenario saga --cluster pg-drill \
  --report artifacts/k8s-restored-saga/report.json
```

`BS_K8S_TEST_CLUSTER` supplies the default for `--cluster` (otherwise `pg`). The
healthy saga needs no injector image and makes no SQL queries. Preflight checks
the current inventory/transfer/gateway Deployment DB values or secret references point at the
requested in-cluster CNPG cluster with their own database/role. It permits the
PITR template's local MinIO archive under `externalClusters`, not remote database
connections. HTTP forwards are created fresh for each invocation.

## Checks and evidence

| Scenario | Actual failure | Required evidence |
| --- | --- | --- |
| `gateway` | Save replicas, scale logistics-gateway to zero, wait until every old pod is gone | Fresh simulated unit reaches inventory and transfer HTTP projections; confirmation succeeds while gateway is down; read-only SQL proves one confirmed allocation and one published RequestTransport; new pod UIDs recover the saga to TRANSPORT_REQUESTED and exactly one carrier shipment |
| `kafka` | Save replicas, scale broker StatefulSet to zero, wait for pod removal | Gateway `/readyz` returns 503 identifying Kafka while `/healthz` stays 200; HTTP confirmation succeeds; one unpublished transport command remains in outbox for at least 30s; no carrier shipment while down; broker/readiness recover, outbox drains, one shipment |
| `keycloak` | Warm matching pod JWKS cache, stop Keycloak Deployment | Previously issued physician token still works on that pod; absent/invalid JWTs return 401; a real new password grant through the still-running HTTPS ingress fails 502/503/504; restored Keycloak issues a fresh accepted token |
| `dlt` | Publish two malformed records to real logistics.commands | Each exact unique key is read back from real logistics.commands.dlt with source/error/type headers; real service DLT counter appears and increases in Prometheus; file-provisioned bs-dlt-not-empty instance fires for this run's unique topic/type with healthy evaluation; a fresh valid business saga succeeds after poison |
| `saga` | Healthy path, no fault | Create a fresh unit in sim-aist, identify its surrogate ID through inventory HTTP using a unique storage location, wait for that ID in transfer candidates, open/confirm obligation, observe transfer's carrier reference and matching carrier shipment, replay confirmation with the same idempotency key |

The carrier oracle counts **all** orders for that obligation, including cancelled
ones, checks the carrier reference/route/quantity, and observes uniqueness for
40 seconds (longer than the default gateway lease). This is bounded evidence for
one business request and replay, not a universal exactly-once guarantee. No
simulator reset or data deletion is used; unique fixture IDs allow repeated runs.

SQL oracles use `kubectl exec` on the selected CNPG primary, `psql -X -qAt`, and a
`BEGIN READ ONLY; SET LOCAL ROLE <own-service-role>` transaction. They never enable
remote superuser access or reuse Compose credentials. The current Deployment's
DB secret reference, host, role and database are checked before each oracle.

Host HTTP probes use loopback Kubernetes port-forwards, as existing kind smoke
does. These probes verify application/auth behavior, not CNI enforcement.
Service-to-service business traffic keeps the existing policies. The restricted,
non-root, read-only injector has no API token and uses the existing kafka-topics
network identity to reach only Kafka; no allow policy is added. The existing
network-policy test remains a separate prerequisite.

## Real alert verification

The gateway now attaches `libs/observability.ConsumerMetrics` to its real consumer.
It records `messaging.consumer.handled`, `messaging.consumer.duration` and
`messaging.consumer.dead_letters` through the already configured runtime meter
provider. The last counter increments **only after** Kafka acknowledges a DLT
write. Other services' unconnected hooks and outbox/lag metrics are outside this
change. No test exporter or synthetic alert rule is installed.

The test reads:

- Grafana `/api/v1/provisioning/alert-rules/bs-dlt-not-empty` to verify UID, file
  provenance, datasource and the existing expression;
- the Prometheus API through Grafana's datasource proxy,
  `/api/datasources/proxy/uid/prometheus/api/v1/query`;
- Grafana `/api/prometheus/grafana/api/v1/rules` for actual runtime rule health,
  firing rule state and this run's `Alerting` instance with a fresh timestamp.

Two poison messages deliberately use the same unique type. `increase(counter[10m])`
cannot detect a counter whose first observed value is already 1, so the test first
observes that real baseline, waits another OTLP interval, and publishes the second
poison. Missing series, NoData/OK, pending state, evaluation error, unrelated
alerts, or mere rule provisioning **never count as success**. The existing rule
expression, severity, and one-minute evaluation interval are unchanged.

Grafana demo credentials default to `admin:admin`; override
`BS_CHAOS_GRAFANA_BASIC` if the stand uses different basic credentials. Secrets and
tokens are not written to the report. The Keycloak ingress probe validates the
stand's own CA from `bs-tls`, including the localhost certificate hostname.

## Cleanup, failures and limits

`artifacts/k8s-chaos/report.json` contains per-scenario `passed`, `failed`, or
`not_run` states, fixture IDs, exact DLT offsets, firing evidence and cleanup
errors. Any failed/not-run selected check gives a nonzero exit. Upload this report
even on failure. Gate PITR on a successful chaos exit.

Each outage uses scale preconditions, restores the original replica count in a
`finally` block, and waits for rollout recovery before returning. Injector Jobs
are deleted and all port-forwards are terminated. SIGINT/SIGTERM trigger cleanup.
If cleanup cannot reach Kubernetes or a controller has been replaced concurrently,
the run fails and starts no further faults. Inspect the reported controller and
restore its saved replica count manually before further work. SIGKILL or runner
loss cannot execute cleanup; the parent should discard that ephemeral cluster.

The Kafka scenario is a controlled broker outage/restart, not process freezing or
packet loss. The gateway scenario exercises a stopped consumer with queued work,
not a kill precisely between carrier acceptance and local commit. The Keycloak
check covers warmed API JWT validation and demo direct grants; browser sessions,
refresh expiry, fresh verifier pods and key rotation are not covered (parent's
browser acceptance covers HTML login). The DLT check also waits for an actual
Grafana notification containing this run's unique type in Mailpit. SMTP and its
two narrow NetworkPolicies are enabled only by the local/ci simulator component.
Automatic resolution after the ten-minute window is not asserted. DLT records and their
short-lived metric/alert instances intentionally remain as test evidence; no fault
remains active after successful completion.

No live scenario, image build, or deployment was executed locally. Local checks:

```sh
python3 -m unittest discover -s tools/chaos -p 'test_*.py' -v
go test ./libs/observability ./libs/messaging ./tools/chaos/inject \
  ./services/logistics-gateway/cmd/logistics-gateway
```

Primary API references consulted: [kubectl scale preconditions](https://kubernetes.io/docs/reference/generated/kubectl/kubectl-commands/#scale),
[Prometheus query API](https://prometheus.io/docs/prometheus/latest/querying/api/),
[Grafana datasource proxy](https://grafana.com/docs/grafana/latest/developer-resources/api-reference/http-api/data_source/#data-source-proxy-calls),
[Grafana rule/instance response definitions](https://github.com/grafana/grafana/blob/v12.0.2/pkg/services/ngalert/api/tooling/definitions/prom.go),
and [Grafana rule-state conversion](https://github.com/grafana/grafana/blob/v12.0.2/pkg/services/ngalert/api/prometheus/api_prometheus.go).
