import base64
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import release


REPOSITORY = "AlchemistBro/blood-supply-pet"
COMMIT = "a" * 40
DIGEST = "sha256:" + "b" * 64
RUN_ID = "1234"


def manifest():
    return {
        "schema_version": 1, "version": "v1.0.0", "source_commit": COMMIT,
        "source_repository": REPOSITORY, "source_ref": "refs/heads/main",
        "platform": "linux/amd64", "candidate_run_id": RUN_ID,
        "images": {f"{release.image_name(REPOSITORY, name)}:1.0.0": DIGEST for name in release.NAMES},
    }


def sbom():
    return {"spdxVersion": "SPDX-2.3", "SPDXID": "SPDXRef-DOCUMENT", "dataLicense": "CC0-1.0",
            "documentNamespace": "https://example.com/sbom", "creationInfo": {"created": "2026-10-26T00:00:00Z", "creators": ["Tool: syft"]},
            "packages": [{"name": "example-module", "SPDXID": "SPDXRef-package"}]}


def provenance(name="audit"):
    source = {"uri": f"https://github.com/{REPOSITORY}.git#{COMMIT}", "digest": {"sha1": COMMIT}}
    return {
        "buildDefinition": {
            "buildType": release.BUILD_TYPE,
            "externalParameters": {
                "configSource": {**source, "path": release.dockerfile(name)},
                "request": {"args": {"build-arg:VERSION": "v1.0.0", "build-arg:SERVICE": name}},
            },
            "resolvedDependencies": [source, {"uri": "pkg:docker/golang", "digest": {"sha256": "c" * 64}}],
        },
        "runDetails": {
            "builder": {"id": f"https://github.com/{REPOSITORY}/actions/runs/{RUN_ID}/attempts/1"},
            "metadata": {"startedOn": "2026-10-26T00:00:00Z", "finishedOn": "2026-10-26T00:01:00Z"},
        },
    }


def envelope(predicate, predicate_type=release.SPDX, digest=DIGEST):
    statement = {"_type": "https://in-toto.io/Statement/v1", "predicateType": predicate_type,
                 "subject": [{"name": "image", "digest": {"sha256": digest.split(":")[1]}}], "predicate": predicate}
    return json.dumps({"payloadType": "application/vnd.in-toto+json",
                       "payload": base64.b64encode(json.dumps(statement).encode()).decode(), "signatures": []})


class ReleaseTests(unittest.TestCase):
    def test_manifest_has_all_services_simulators_and_minio(self):
        images = release.validate_manifest(manifest(), REPOSITORY, COMMIT, "v1.0.0", RUN_ID)
        self.assertEqual(len(images), 13)
        self.assertEqual(len(release.SERVICES), 12)
        self.assertIn("ghcr.io/alchemistbro/blood-supply-minio:1.0.0", images)
        self.assertEqual(release.dockerfile("minio"), "deploy/docker/Minio.Dockerfile")
        root = Path(__file__).resolve().parents[2]
        discovered = {p.name for p in (root / "services").iterdir() if (p / "cmd" / p.name / "main.go").exists()}
        self.assertEqual(discovered, set(release.SERVICES))

    def test_manifest_rejects_wrong_commit_repository_version_run_platform_and_missing_image(self):
        changes = {"source_commit": "d" * 40, "source_repository": "other/repo", "version": "v1.0.1",
                   "candidate_run_id": "5678", "platform": "linux/arm64", "source_ref": "refs/heads/other", "schema_version": 2}
        for key, value in changes.items():
            with self.subTest(key=key), self.assertRaises(ValueError):
                candidate = manifest()
                candidate[key] = value
                release.validate_manifest(candidate, REPOSITORY, COMMIT, "v1.0.0", RUN_ID)
        candidate = manifest()
        candidate["images"].pop(next(iter(candidate["images"])))
        with self.assertRaises(ValueError):
            release.validate_manifest(candidate, REPOSITORY, COMMIT)

    def test_manifest_rejects_tags_short_digests_and_extra_images(self):
        for digest in ("latest", "sha256:1234", "sha256:" + "G" * 64, "sha512:" + "b" * 64):
            with self.subTest(digest=digest), self.assertRaises(ValueError):
                candidate = manifest()
                candidate["images"][next(iter(candidate["images"]))] = digest
                release.validate_manifest(candidate, REPOSITORY, COMMIT)
        candidate = manifest()
        candidate["images"]["ghcr.io/other/image:1.0.0"] = DIGEST
        with self.assertRaises(ValueError):
            release.validate_manifest(candidate, REPOSITORY, COMMIT)

    def test_version_rejects_shell_injection_branches_prereleases_and_aliases(self):
        for value in ("main", "1.0.0", "v01.0.0", "v1.0.0-rc1", "v1.0.0;echo bad", "v1.0.0\n", "v1.0"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                release.version_number(value)

    def test_version_tag_guard_allows_only_absent_or_identical_digest(self):
        release.guard_tag(None, DIGEST)
        release.guard_tag(DIGEST, DIGEST)
        with self.assertRaisesRegex(ValueError, "overwrite"):
            release.guard_tag("sha256:" + "e" * 64, DIGEST)

    def test_spdx_requires_real_package_inventory_and_metadata(self):
        release.validate_sbom(sbom())
        for key, value in (("packages", []), ("creationInfo", {}), ("SPDXID", "wrong"), ("dataLicense", "MIT")):
            candidate = sbom()
            candidate[key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                release.validate_sbom(candidate)

    def test_provenance_binds_source_dockerfile_build_parameters_and_builder_run(self):
        release.validate_provenance(provenance(), REPOSITORY, COMMIT, "audit", "v1.0.0", RUN_ID)
        release.validate_provenance(provenance("minio"), REPOSITORY, COMMIT, "minio", "v1.0.0", RUN_ID)
        mutations = [
            lambda p: p["buildDefinition"]["externalParameters"]["configSource"]["digest"].update(sha1="d" * 40),
            lambda p: p["buildDefinition"]["externalParameters"]["configSource"].update(uri="https://github.com/other/repo.git"),
            lambda p: p["buildDefinition"]["externalParameters"]["configSource"].update(path="wrong/Dockerfile"),
            lambda p: p["buildDefinition"]["externalParameters"]["request"]["args"].update({"build-arg:SERVICE": "web"}),
            lambda p: p["buildDefinition"]["externalParameters"]["request"]["args"].update({"build-arg:VERSION": "v2.0.0"}),
            lambda p: p["buildDefinition"].update(resolvedDependencies=[]),
            lambda p: p["runDetails"]["builder"].update(id="https://example.com/builder"),
            lambda p: p["runDetails"].update(metadata={}),
        ]
        for mutation in mutations:
            candidate = provenance()
            mutation(candidate)
            with self.assertRaises(ValueError):
                release.validate_provenance(candidate, REPOSITORY, COMMIT, "audit", "v1.0.0", RUN_ID)

    def test_attestation_checks_verified_predicate_type_subject_digest_and_content(self):
        signer = release.identity(REPOSITORY, "images", "refs/heads/main")
        reference = release.image_name(REPOSITORY, "audit") + "@" + DIGEST
        with mock.patch.object(release, "run", return_value=envelope(sbom())) as execute:
            release.verify_attestation(reference, signer, release.SPDX, sbom())
            command = execute.call_args.args
            self.assertIn("--certificate-identity", command)
            self.assertIn(signer, command)
            self.assertIn(release.ISSUER, command)
            self.assertFalse(any("insecure" in arg or "ignore" in arg for arg in command))
        for output in (envelope(sbom(), digest="sha256:" + "d" * 64), envelope(sbom(), predicate_type=release.SLSA), envelope({}), ""):
            with self.subTest(output=output[:80]), mock.patch.object(release, "run", return_value=output), self.assertRaises(ValueError):
                release.verify_attestation(reference, signer, release.SPDX, sbom())

    def test_multiple_verified_envelopes_are_supported(self):
        statements = release.verified_statements(envelope(sbom()) + "\n" + envelope(provenance(), release.SLSA))
        self.assertEqual(len(statements), 2)

    def test_cosign_failure_prevents_acceptance(self):
        with mock.patch.object(release, "run", side_effect=release.subprocess.CalledProcessError(1, "cosign")):
            with self.assertRaises(release.subprocess.CalledProcessError):
                release.verify_image("image@" + DIGEST, "trusted-identity", sbom(), provenance())

    def test_checksum_inventory_detects_tampering_missing_files_and_traversal(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            for name in release.NAMES:
                for suffix in ("image.json", "spdx.json", "provenance.json", "verification.json"):
                    (directory / f"{name}.{suffix}").write_text("{}", encoding="utf-8")
            (directory / "images.json").write_text("{}", encoding="utf-8")
            release.checksums(directory, "candidate.SHA256SUMS")
            release.verify_checksums(directory)
            (directory / "images.json").write_text('{"changed":true}', encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "checksum differs"):
                release.verify_checksums(directory)
            (directory / "candidate.SHA256SUMS").write_text("a" * 64 + "  ../outside.json\n", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "invalid candidate checksum"):
                release.verify_checksums(directory)

    def test_promotion_rejects_other_candidate_sha_before_writes(self):
        candidate_run = {"conclusion": "success", "status": "completed", "path": ".github/workflows/release.yml",
                         "event": "workflow_dispatch", "head_branch": "main", "head_sha": "d" * 40,
                         "repository": {"full_name": REPOSITORY}}
        args = release.argparse.Namespace(version="v1.0.0", candidate_run_id=RUN_ID, output=Path("unused"))
        with mock.patch.dict(os.environ, {"GITHUB_REPOSITORY": REPOSITORY, "GITHUB_SHA": COMMIT, "GITHUB_REF": "refs/tags/v1.0.0"}), \
                mock.patch.object(release, "api_json", return_value=candidate_run), mock.patch.object(release, "run") as execute:
            with self.assertRaisesRegex(ValueError, "source, or repository differs"):
                release.promote(args)
            execute.assert_not_called()

    def test_k8s_gate_binds_exact_manifest_candidate_source_and_all_gates(self):
        k8s_run = {"status": "completed", "conclusion": "success", "event": "workflow_dispatch",
                   "path": ".github/workflows/k8s.yml", "head_sha": COMMIT, "repository": {"full_name": REPOSITORY}}
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "images.json"
            release.write_json(path, manifest())
            evidence = {"schema_version": 1, "images_run_id": RUN_ID, "source_commit": COMMIT,
                        "candidate_version": "v1.0.0", "images_manifest_sha256": release.hashlib.sha256(path.read_bytes()).hexdigest(),
                        "readiness": True, "startup": True, "startup_seconds": 600, "chaos": True, "drill": True}
            release.validate_k8s_evidence(evidence, path, REPOSITORY, COMMIT, "v1.0.0", RUN_ID, k8s_run)
            changes = {"images_run_id": "5555", "source_commit": "d" * 40, "candidate_version": "v1.0.1",
                       "images_manifest_sha256": "0" * 64, "readiness": False, "startup": False,
                       "chaos": False, "drill": False, "schema_version": 2}
            for key, value in changes.items():
                with self.subTest(key=key), self.assertRaises(ValueError):
                    release.validate_k8s_evidence({**evidence, key: value}, path, REPOSITORY, COMMIT, "v1.0.0", RUN_ID, k8s_run)
            for seconds in (600.01, -1, float("nan"), float("inf"), True, "599"):
                with self.subTest(seconds=seconds), self.assertRaises(ValueError):
                    release.validate_k8s_evidence({**evidence, "startup_seconds": seconds}, path, REPOSITORY, COMMIT, "v1.0.0", RUN_ID, k8s_run)
            for key, value in (("head_sha", "d" * 40), ("path", ".github/workflows/ci.yml"), ("conclusion", "failure")):
                with self.subTest(run_key=key), self.assertRaises(ValueError):
                    release.validate_k8s_evidence(evidence, path, REPOSITORY, COMMIT, "v1.0.0", RUN_ID, {**k8s_run, key: value})

    def test_conflicting_version_tag_stops_before_any_image_publication(self):
        candidate_run = {"conclusion": "success", "status": "completed", "path": ".github/workflows/release.yml",
                         "event": "workflow_dispatch", "head_branch": "main", "head_sha": COMMIT,
                         "repository": {"full_name": REPOSITORY}}
        args = release.argparse.Namespace(version="v1.0.0", candidate_run_id=RUN_ID, k8s_run_id="5678",
                                          k8s_evidence=Path("unused"), output=Path("unused"))
        with mock.patch.dict(os.environ, {"GITHUB_REPOSITORY": REPOSITORY, "GITHUB_SHA": COMMIT, "GITHUB_REF": "refs/tags/v1.0.0"}), \
                mock.patch.object(release, "api_json", side_effect=[candidate_run, None, {}]), \
                mock.patch.object(release, "run") as execute, \
                mock.patch.object(release, "verify_checksums"), \
                mock.patch.object(release, "read_json", side_effect=[manifest(), {}]), \
                mock.patch.object(release, "validate_k8s_evidence"), \
                mock.patch.object(release, "verify_manifest_images"), \
                mock.patch.object(release, "registry_digest", side_effect=[None] * 12 + ["sha256:" + "e" * 64]), \
                mock.patch.object(release, "sign_image") as sign:
            with self.assertRaisesRegex(ValueError, "overwrite"):
                release.promote(args)
            sign.assert_not_called()
            self.assertEqual(execute.call_count, 1)
            self.assertEqual(execute.call_args.args[:2], ("cosign", "verify-blob"))

    def test_inspect_rejects_wrong_or_multiple_runtime_platforms(self):
        for platforms in ([{"architecture": "arm64", "os": "linux"}], [{"architecture": "amd64", "os": "linux"}] * 2, []):
            index = {"manifests": [{"platform": p} for p in platforms]}
            with mock.patch.object(release, "run", return_value=json.dumps(index)), self.assertRaises(ValueError):
                release.inspect_predicates("image@" + DIGEST)


if __name__ == "__main__":
    unittest.main()
