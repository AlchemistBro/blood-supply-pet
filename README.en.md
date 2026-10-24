# Blood Supply

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

For a synthetic local demo, install Docker, Go 1.27 and Task, then run `task up` and `task seed` from Git Bash
on Windows or a shell on Linux. Open `https://localhost`. Demo users share password `demo`:
`physician1`, `operator-c001`, `operator-c002`, `analyst1`, `duty1`, `it1`.
See the [Russian demo walkthrough](README.md#демо-за-5-минут).

Kubernetes: `task k8s:up`, `task seed:kind`, `task k8s:smoke`; an isolated PITR drill is available as
`task k8s:drill`. Load and resilience scenarios are provided by `task load` and `task chaos`.
These commands affect a demo environment and require its dependencies.

Current validation consists of builds, vet, linters, generated code, contract compatibility, authorization metadata
and static Kubernetes checks. Tests, Docker workloads, load measurements and recovery drills have not been run
at the owner's request. No public deployment or release is claimed. See [acceptance status](docs/perf/acceptance-status.md).

[Architecture](docs/architecture/c4-containers.md) · [Saga](docs/architecture/saga-confirmation.md) ·
[Decisions](docs/adr/README.md) · [Plan](docs/PLAN.md) · [MIT license](LICENSE).
