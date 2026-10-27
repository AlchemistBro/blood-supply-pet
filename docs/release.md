# Release v1.0.0

Published release: [v1.0.0](https://github.com/AlchemistBro/blood-supply-pet/releases/tag/v1.0.0),
source `9a3093bdbeaca2b7f96188ec9f000fb41bae852f`, platform `linux/amd64`.
[Promotion](https://github.com/AlchemistBro/blood-supply-pet/actions/runs/40420225780)
preserved all 13 tested image digests. The release contains 73 assets;
all 71 checksum entries were checked after download. See the
[acceptance report](perf/release-v1.0.0-2026-10-26.md) for CI/E2E, cold startup,
chaos/alert delivery, application operations after PITR, load results and their limits.

Release publication has two explicit operations: build a candidate from `main`, then promote that tested candidate from an existing version tag. Neither workflow publishes on a `main` push. `images.yml` is a reusable workflow called by `release.yml`; it has no direct push or dispatch trigger. Repository workflow enablement, dispatches, tag creation, and package visibility are maintainer operations.

The inventory contains 12 application images (`audit`, `demand`, `inventory`, `logistics-gateway`, `matching`, `notification`, `planning`, `sim-aist`, `sim-emergency`, `sim-logistics`, `transfer`, `web`) and the MinIO platform image. The application images use `deploy/docker/Dockerfile`; MinIO uses `deploy/docker/Minio.Dockerfile` and includes both `minio` and `mc`. All release images target **linux/amd64**. This release does not rebuild or mirror the other upstream platform images.

## Candidate and deployment gate

1. Finish the source checks and merge the intended release commit to `main`. Check the service inventory and run the local release checks below. Enable the release workflow when ready; its current GitHub enablement state is independent of these files. GitHub must allow GHCR publication with `GITHUB_TOKEN` and Sigstore OIDC token issuance.
2. Dispatch the candidate build with an explicit stable version:

   ```sh
   gh workflow run release.yml --ref main -f phase=candidate -f version=v1.0.0
   ```

3. Wait for the candidate run to succeed. Save its run ID as `CANDIDATE_RUN_ID`. Download the complete evidence artifact:

   ```sh
   gh run download "$CANDIDATE_RUN_ID" --name release-candidate-v1.0.0 --dir candidate
   python3 tools/release/release.py verify-manifest --manifest candidate/images.json \
     --repository AlchemistBro/blood-supply-pet --commit "$EXPECTED_COMMIT" --online
   ```

4. Run the Kubernetes gate at that exact source commit, selecting this candidate run through the Kubernetes workflow's `images_run_id` input. Its downloader needs `actions: read` and an explicit cross-run token. Select the artifact `release-candidate-v1.0.0`; `images.json` is at its root. Check `source_commit` against the Kubernetes run's `github.sha` before rendering manifests. The validator above can also be run without `--online` for a local schema, inventory, and source check.
5. Use `BS_K8S_RELEASE_MANIFEST` to select the downloaded manifest for deployment. Render all 13 images as `repository@sha256:...`, including the MinIO server and setup job. Kubelets pull directly from GHCR. For private packages, supply a scoped image pull secret from authenticated Docker registry configuration; do not put credentials in this manifest, release assets, or logs. A Docker credential helper may keep credentials outside `config.json`; materialize the scoped secret through the deployment tooling's supported credential path.
6. Record cold deployment elapsed time from **before kind creation** through image pulls and workload readiness, with a gate of **600 seconds or less**. Record source compilation separately, including the MinIO source build. A warm cluster, preloaded application layers, or time excluding kind creation does not establish the cold deployment target. The release tooling itself does not measure deployment time or prove this performance target.

Candidates receive unique tags of the form `candidate-1.0.0-<commit>-<run>-<attempt>`. Candidate jobs never write `1.0.0`, minor, major, edge, or latest tags. Keep registry manifests, layers, and Cosign referrers available until testing and promotion finish. Candidate evidence artifacts expire after 30 days. Artifact uploads refuse replacement; use a fresh candidate dispatch if rerunning completed jobs would replace an existing artifact. A successful candidate run ID therefore identifies a fixed manifest.

## `images.json` contract

The same bytes are used for candidate testing and final release publication:

```json
{
  "schema_version": 1,
  "version": "v1.0.0",
  "source_commit": "<40 lowercase hexadecimal characters>",
  "source_repository": "AlchemistBro/blood-supply-pet",
  "source_ref": "refs/heads/main",
  "platform": "linux/amd64",
  "candidate_run_id": "<decimal GitHub Actions run ID>",
  "images": {
    "ghcr.io/alchemistbro/blood-supply-audit:1.0.0": "sha256:<64 lowercase hexadecimal characters>",
    "ghcr.io/alchemistbro/blood-supply-minio:1.0.0": "sha256:<64 lowercase hexadecimal characters>"
  }
}
```

The example abbreviates the map: real manifests contain exactly all 13 images. `source_commit`, rather than `revision`, is the source identity field. `version` includes `v`; container tag keys omit it. Values identify the complete OCI image index, including BuildKit attestations, rather than a mutable tag or an individual platform manifest. Final tag keys may not exist while testing the candidate. Split each key at its last colon to remove the version tag, then append `@` and the digest value. Validate the expected repository, full commit, version, platform, inventory, and digest syntax; never silently fall back to building from source if validation fails.

## Promotion

For a local Compose demonstration with published application images, keep PostgreSQL local and select the version explicitly:

```powershell
$env:IMAGE_TAG = '1.0.0'
docker compose -f deploy/compose/compose.yaml --env-file deploy/compose/.env --profile services up -d --no-build --pull missing --wait
```

Create `deploy/compose/.env` using the setup instructions in [demo.md](demo.md) first, and authenticate Docker to GHCR if the packages are private. This starts local containers and keeps the demo database on your computer. Use `stop` to pause them; `down -v` removes their database volumes.

After the exact candidate passes CI and the Kubernetes gate, the maintainer creates and pushes `v1.0.0` at its `source_commit`. The workflows never create or move Git tags. Protect release tags and restrict GHCR write access so other writers cannot bypass the workflow's checks.

Dispatch promotion **with the version tag selected as the workflow ref** and the tested candidate run explicitly selected:

```sh
gh workflow run release.yml --ref v1.0.0 -f phase=promote -f version=v1.0.0 \
  -f candidate_run_id="$CANDIDATE_RUN_ID" -f k8s_run_id="$K8S_RUN_ID"
```

Promotion rejects a branch ref, malformed version, unsuccessful candidate or Kubernetes run, unexpected workflow, different repository or source commit, altered evidence, or an existing GitHub Release. The candidate must originate from this repository's `main` release workflow. The Kubernetes run must be a successful dispatch of `.github/workflows/k8s.yml` at the exact candidate source commit. Its `kubernetes-diagnostics` artifact must contain `dist/k8s-release-evidence.json` with this contract:

```json
{
  "schema_version": 1,
  "images_run_id": "<candidate run ID as a string>",
  "source_commit": "<40 lowercase hexadecimal characters>",
  "candidate_version": "v1.0.0",
  "images_manifest_sha256": "<64 lowercase hexadecimal characters, no prefix>",
  "readiness": true,
  "startup": true,
  "startup_seconds": 599.0,
  "chaos": true,
  "drill": true
}
```

`images_manifest_sha256` hashes the original downloaded `images.json` bytes, preserving whitespace and line endings. All four gate booleans must be JSON `true`; `startup_seconds` must be a finite number from zero through 600 inclusive. Promotion verifies the run's API metadata and this evidence before any image signing or version-tag publication. It includes the Kubernetes evidence and run URL in the final signed release inventory. The Kubernetes producer must report the full cold wall time and emit false gates when checks are skipped or fail; a successful unrelated run or a run testing another manifest is rejected.

All 13 image signatures, SBOMs, provenance predicates, and prospective version tags are checked before writing any version tag. A tag can be absent or already resolve to the exact candidate digest. A different existing digest aborts promotion; a manual rerun at another source commit cannot replace it. Tags that already resolve to the candidate digest are left in place. The workflow serializes releases of the same version and rechecks immediately before each tag write. GHCR does not provide a compare-and-swap operation for these writes; repository access restrictions must exclude competing external tag writers.

Promotion adds signatures and signed attestations under the version-tag identity, copies the existing image indexes to `1.0.0`, and verifies every copied digest. **It never rebuilds images.** It publishes no rolling aliases. A release is created as a draft with all evidence attached, then made public only after image verification and asset upload succeed. If an upload fails after draft creation, inspect and recover that draft manually; the workflow refuses to replace an existing release. If an earlier registry operation fails, rerun the failed promotion with the same candidate: existing equal digests are accepted. Registry publication is not atomic across 13 images; a failure can leave partial version tags, but cannot publish the GitHub Release through the verification gate.

## Signatures, SBOMs, and provenance

Builds use an immutable Git context at the candidate commit, BuildKit SLSA v1 provenance in `mode=max`, and an explicitly pinned Syft scanner for SPDX generation. The SBOM describes packages in the final runtime image, including Go module metadata discoverable in the compiled binaries; it does not claim to inventory every build-stage tool or source file. Base image and Git source digests are recorded in provenance. Base image tags in the shared Dockerfiles can still change between candidate builds, so reproducibility is not claimed. Testing and promotion always use the fixed candidate digests.

BuildKit attestations are part of the signed index, but that alone is not treated as a Cosign attestation. The tooling extracts the SBOM and provenance from each immutable image, validates their contents, signs the image digest, and separately creates keyless Cosign attestations for both predicates. It verifies the exact certificate identity and GitHub Actions OIDC issuer with Cosign's certificate and transparency-log checks enabled. It then decodes only the verified DSSE envelopes and checks predicate type, subject digest, and predicate equality. SPDX must include document metadata and a nonempty package inventory. Provenance must identify the expected Git repository, commit, Dockerfile, service/version arguments, resolved base image digests, candidate run builder ID, and build timestamps. These are workflow-level attestations, not a claim of isolated SLSA Build Level 3 generation.

Fulcio uses the **called reusable workflow** as the candidate image signing identity:

```text
https://github.com/AlchemistBro/blood-supply-pet/.github/workflows/images.yml@refs/heads/main
```

The candidate checksum inventory is signed by `release.yml@refs/heads/main`. Promoted image signatures, SBOM/provenance attestations, and final checksum inventory use:

```text
https://github.com/AlchemistBro/blood-supply-pet/.github/workflows/release.yml@refs/tags/v1.0.0
```

The issuer is always `https://token.actions.githubusercontent.com`. Do not broaden identities to arbitrary branches or disable certificate, subject, or transparency-log verification. Registry checks need authenticated `docker login ghcr.io` when packages are private. Online verification requires Docker Buildx v0.38.1, Cosign v3.1.4, Python 3, and access to GHCR and Sigstore services.

Release assets include `images.json`, all 13 SPDX documents, all 13 BuildKit provenance predicates, per-image candidate records and verification evidence, `candidate.SHA256SUMS` with its `candidate.sigstore.json` bundle, and final `SHA256SUMS` with its `release.sigstore.json` bundle. Verification JSON files record successful command output; the registry signatures and Sigstore bundles provide the cryptographic evidence.

Verify the final asset inventory and one image using the exact release identity:

```sh
cosign verify-blob SHA256SUMS --bundle release.sigstore.json \
  --certificate-identity https://github.com/AlchemistBro/blood-supply-pet/.github/workflows/release.yml@refs/tags/v1.0.0 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --check SHA256SUMS
cosign verify 'ghcr.io/alchemistbro/blood-supply-minio@sha256:<digest-from-images.json>' \
  --certificate-identity https://github.com/AlchemistBro/blood-supply-pet/.github/workflows/release.yml@refs/tags/v1.0.0 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

`verify-manifest --online` checks all candidate image signatures and signed predicates against the manifest's exact source commit. Published images retain these candidate signatures after promotion, as well as the additional version-tag signatures. For direct release attestation inspection, use `cosign verify-attestation` with the version-tag identity and `--type https://spdx.dev/Document` or `--type https://slsa.dev/provenance/v1`; an exit code alone does not replace checking the verified predicate contents.

## Permissions and local validation

The default token has only `contents: read`. Candidate image jobs additionally receive `packages: write` and `id-token: write`. Candidate assembly receives only OIDC signing permission beyond read access. Promotion adds `actions: read` for cross-run artifact access, `packages: write`, `id-token: write`, and `contents: write` for the GitHub Release. No long-lived signing key, personal token, or `attestations: write` permission is needed: the attestations are stored in GHCR through Cosign, rather than GitHub's separate artifact-attestations API. GHCR packages must permit this repository's workflow token to write; private Kubernetes pulls require their own read-scoped credentials.

```sh
python3 -B -m unittest discover -s tools/release -p 'test_*.py' -v
actionlint .github/workflows/images.yml .github/workflows/release.yml
```

The tests exercise manifest/source rejection, exact service coverage, version tag collision protection, invalid provenance and empty SBOM rejection, verified DSSE subject/content matching, checksum tampering, and early promotion failure. They do not publish images, create tags, or change GitHub state. A real candidate run remains necessary to validate hosted-runner OIDC, registry permissions, and the complete build/sign/upload path.

Official behavior references: [Docker attestation storage](https://docs.docker.com/build/metadata/attestations/attestation-storage/), [Docker SBOM generation and extraction](https://docs.docker.com/build/metadata/attestations/sbom/), [BuildKit SLSA definitions](https://github.com/moby/buildkit/blob/v0.34.1/docs/attestations/slsa-definitions.md), [digest-preserving image index copies](https://docs.docker.com/reference/cli/docker/buildx/imagetools/create/), [Sigstore verification](https://docs.sigstore.dev/cosign/verifying/verify/), [Fulcio GitHub identity claims](https://github.com/sigstore/fulcio/blob/main/docs/oidc.md), and [GitHub workflow permissions](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#permissions).
