# Blood Supply

![Request screen: matching variants with explanations](docs/ui/screens/request.png)

A Go microservices project for distributing donated blood components between clinics: inventory and stock norms,
territorial demand, emergency replenishment planning, clinician requests, transfer confirmation and transport.
The domain comes from an RTU MIREA architecture project. Data, thresholds and matching rules are synthetic.

Nine services use PostgreSQL databases per service, Kafka transactional outbox/inbox, OpenAPI, OIDC/Keycloak,
Go server rendering with HTMX, and OpenTelemetry. The only synchronous backend call is transfer → inventory
for a current unit status check; the web BFF calls service APIs with the user's JWT.

Transfer confirmation uses stock locks, compare-and-swap transitions, idempotency keys and compensation.
The logistics gateway reconciles uncertain outcomes before retrying an order. Clinician case numbers are encrypted;
reading a case requires a separate audited operation. Patient allocations reach the source system through a durable
queue. Transport violations block clinical use and raise an escalation; closing the escalation does not remove the block.

For a synthetic local demo, install Docker, Go 1.27.3+ and Task, then run `task up` and `task seed` from Git Bash
on Windows or a shell on Linux. Open `https://localhost`. Demo users share password `demo`:
`physician1`, `operator-c001`, `operator-c002`, `analyst1`, `duty1`, `it1`.
See the [demo walkthrough](docs/demo.md) (Russian). `physician1` belongs to C002;
use a remote C001 offer with `operator-c001`, then `operator-c002` for receipt. Other source clinics require `duty1`.
Three-role browser login and the CSV demand workflow have been validated; the full manual walkthrough remains separate.

Kubernetes: `task k8s:up`, `task seed:kind`, `task k8s:smoke`; an isolated PITR drill is available as
`task k8s:drill`. Load and resilience scenarios are provided by `task load` and `task chaos`.
These commands affect a demo environment and require its dependencies.

Validation on 26–27 October 2026: all six CI jobs passed, including unit/race, PostgreSQL/Kafka integration,
contracts, lint/generation, security and builds. Ten Compose E2E scenarios and a 50-user load test passed.
The Kubernetes acceptance run checked gateway/Kafka/Keycloak outages, a real dead-letter record,
Grafana alert delivery to Mailpit, and application operations against a PITR-restored database.
Cold deployment of prebuilt digest-pinned images took 410 seconds; source compilation is measured separately.
See [acceptance status](docs/perf/acceptance-status.md) and [release acceptance](docs/perf/release-v1.0.0-2026-10-26.md)
for exact runs and limitations. Some observability panels, additional Kubernetes linters and the full manual demo
remain separate checklist items.

[Release v1.0.0 is published](https://github.com/AlchemistBro/blood-supply-pet/releases/tag/v1.0.0):
12 service images and MinIO preserve the tested candidate digests, with verified keyless signatures,
SPDX SBOMs and source-bound provenance. The release includes 73 evidence assets;
[release verification](docs/release.md) describes their use. Public hosting is outside the project scope.

[Architecture](docs/architecture/c4-containers.md) · [Saga](docs/architecture/saga-confirmation.md) ·
[Decisions](docs/adr/README.md) · [Plan](docs/PLAN.md) · [MIT license](LICENSE).
