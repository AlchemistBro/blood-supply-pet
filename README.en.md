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
The walkthrough is prepared from the implementation; a live end-to-end demonstration has not been validated.

Kubernetes: `task k8s:up`, `task seed:kind`, `task k8s:smoke`; an isolated PITR drill is available as
`task k8s:drill`. Load and resilience scenarios are provided by `task load` and `task chaos`.
These commands affect a demo environment and require its dependencies.

Validation on 24 October 2026: 1775 ordinary tests/subtests and 1839 with PostgreSQL passed;
15 Kafka-dependent tests were skipped. Builds, vet, linters, contract compatibility and authorization checks passed.
Govulncheck for Windows/Linux found no vulnerabilities in called code or imported packages; an unused OpenPGP
module advisory remains documented. Gitleaks passed with a narrowly scoped exception for a public synthetic demo key.
See [acceptance status](docs/perf/acceptance-status.md) and the [security report](docs/architecture/security-local-2026-10-24.md).

Docker workloads, browser/end-to-end tests, load measurements, image scans and recovery drills have not been run.
Public hosting is outside the project scope; no release is claimed.

[Architecture](docs/architecture/c4-containers.md) · [Saga](docs/architecture/saga-confirmation.md) ·
[Decisions](docs/adr/README.md) · [Plan](docs/PLAN.md) · [MIT license](LICENSE).
