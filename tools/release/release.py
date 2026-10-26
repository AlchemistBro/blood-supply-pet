#!/usr/bin/env python3
"""Candidate images, verification, and digest-preserving release promotion."""

import argparse
import base64
import hashlib
import json
import math
import os
from pathlib import Path
import re
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request


SERVICES = (
    "audit", "demand", "inventory", "logistics-gateway", "matching",
    "notification", "planning", "sim-aist", "sim-emergency", "sim-logistics",
    "transfer", "web",
)
NAMES = (*SERVICES, "minio")
SPDX = "https://spdx.dev/Document"
SLSA = "https://slsa.dev/provenance/v1"
BUILD_TYPE = "https://github.com/moby/buildkit/blob/master/docs/attestations/slsa-definitions.md"
ISSUER = "https://token.actions.githubusercontent.com"
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")
COMMIT = re.compile(r"[0-9a-f]{40}\Z")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def version_number(version):
    require(re.fullmatch(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", version),
            "version must be stable SemVer including v, for example v1.0.0")
    return version[1:]


def image_name(repository, name):
    require(re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository), "invalid repository")
    require(name in NAMES, "unknown image")
    return f"ghcr.io/{repository.split('/')[0].lower()}/blood-supply-{name}"


def dockerfile(name):
    return "deploy/docker/Minio.Dockerfile" if name == "minio" else "deploy/docker/Dockerfile"


def run(*command):
    return subprocess.run(command, check=True, text=True, encoding="utf-8", stdout=subprocess.PIPE).stdout


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def read_json(path):
    return json.loads(path.read_text(encoding="utf-8"))


def identity(repository, workflow, ref):
    return f"https://github.com/{repository}/.github/workflows/{workflow}.yml@{ref}"


def trust_flags(signer):
    return ["--certificate-identity", signer, "--certificate-oidc-issuer", ISSUER]


def validate_manifest(manifest, repository, commit, version=None, candidate_run_id=None):
    require(manifest.get("schema_version") == 1, "unsupported manifest schema")
    require(COMMIT.fullmatch(commit), "expected commit must be a full Git SHA")
    require(manifest.get("source_commit") == commit, "candidate source commit differs from requested commit")
    require(manifest.get("source_repository") == repository, "candidate repository differs")
    require(manifest.get("source_ref") == "refs/heads/main", "candidate must originate from main")
    require(manifest.get("platform") == "linux/amd64", "unsupported candidate platform")
    number = version_number(manifest.get("version", ""))
    if version is not None:
        require(manifest["version"] == version, "candidate version differs")
    require(re.fullmatch(r"[1-9][0-9]*", str(manifest.get("candidate_run_id", ""))), "invalid candidate run ID")
    if candidate_run_id is not None:
        require(manifest["candidate_run_id"] == candidate_run_id, "candidate run ID differs")
    images = manifest.get("images")
    expected = {f"{image_name(repository, name)}:{number}" for name in NAMES}
    require(isinstance(images, dict) and set(images) == expected, "manifest must contain exactly all 13 versioned images")
    require(all(isinstance(value, str) and DIGEST.fullmatch(value) for value in images.values()),
            "every image must have a full sha256 digest")
    return images


def validate_sbom(sbom):
    require(isinstance(sbom, dict), "missing SPDX SBOM")
    require(sbom.get("spdxVersion") in ("SPDX-2.2", "SPDX-2.3"), "unsupported SPDX version")
    require(sbom.get("SPDXID") == "SPDXRef-DOCUMENT", "invalid SPDX document ID")
    require(sbom.get("dataLicense") == "CC0-1.0" and sbom.get("documentNamespace"), "missing SPDX metadata")
    info = sbom.get("creationInfo", {})
    require(info.get("created") and info.get("creators"), "missing SPDX creation metadata")
    packages = sbom.get("packages", [])
    require(packages and all(p.get("name") and p.get("SPDXID") for p in packages), "empty or invalid SPDX package inventory")


def validate_provenance(provenance, repository, commit, name, version, candidate_run_id):
    require(isinstance(provenance, dict), "missing BuildKit provenance")
    definition = provenance.get("buildDefinition", {})
    require(definition.get("buildType") == BUILD_TYPE, "unexpected provenance builder type")
    external = definition.get("externalParameters", {})
    source = external.get("configSource", {})
    source_url = f"https://github.com/{repository}.git"
    # Git references can be represented as URL fragments; the material digest is authoritative.
    require(source.get("uri", "").split("#")[0] == source_url, "provenance source repository differs")
    require(source.get("digest", {}).get("sha1") == commit, "provenance source commit differs")
    require(source.get("path") == dockerfile(name), "provenance Dockerfile differs")
    parameters = external.get("request", {}).get("args", {})
    require(parameters.get("build-arg:VERSION") == version, "provenance version differs")
    if name != "minio":
        require(parameters.get("build-arg:SERVICE") == name, "provenance service differs")
    dependencies = definition.get("resolvedDependencies", [])
    require(any(d.get("uri", "").split("#")[0] == source_url and d.get("digest", {}).get("sha1") == commit
                for d in dependencies), "provenance lacks resolved Git source")
    require(any(re.fullmatch(r"[0-9a-f]{64}", d.get("digest", {}).get("sha256", "")) for d in dependencies),
            "provenance lacks resolved base image digests")
    details = provenance.get("runDetails", {})
    builder = details.get("builder", {}).get("id", "")
    prefix = f"https://github.com/{repository}/actions/runs/{candidate_run_id}/attempts/"
    require(builder.startswith(prefix) and re.fullmatch(r"[1-9][0-9]*", builder[len(prefix):]),
            "provenance builder does not identify candidate run")
    metadata = details.get("metadata", {})
    require(metadata.get("startedOn") and metadata.get("finishedOn"), "provenance lacks build timestamps")


def inspect_predicates(reference):
    index = json.loads(run("docker", "buildx", "imagetools", "inspect", "--raw", reference))
    platforms = [m.get("platform", {}) for m in index.get("manifests", [])
                 if m.get("platform", {}).get("architecture") != "unknown"]
    require(platforms == [{"architecture": "amd64", "os": "linux"}], "image must have exactly one linux/amd64 platform")
    sbom = json.loads(run("docker", "buildx", "imagetools", "inspect", reference,
                          "--format", "{{ json .SBOM.SPDX }}"))
    provenance = json.loads(run("docker", "buildx", "imagetools", "inspect", reference,
                                "--format", "{{ json .Provenance.SLSA }}"))
    return sbom, provenance


def verified_statements(output):
    """Cosign 3 verify-attestation emits one verified DSSE envelope per JSON line."""
    decoder = json.JSONDecoder()
    remaining = output.strip()
    statements = []
    while remaining:
        envelope, end = decoder.raw_decode(remaining)
        require(isinstance(envelope, dict) and envelope.get("payloadType") == "application/vnd.in-toto+json",
                "unexpected verified attestation envelope")
        statements.append(json.loads(base64.b64decode(envelope["payload"], validate=True)))
        remaining = remaining[end:].strip()
    require(statements, "cosign returned no verified attestations")
    return statements


def verify_attestation(reference, signer, predicate_type, expected):
    output = run("cosign", "verify-attestation", *trust_flags(signer), "--type", predicate_type, reference)
    digest = reference.rsplit("@sha256:", 1)[1]
    for statement in verified_statements(output):
        if (statement.get("_type") in ("https://in-toto.io/Statement/v0.1", "https://in-toto.io/Statement/v1")
                and statement.get("predicateType") == predicate_type
                and any(s.get("digest", {}).get("sha256") == digest for s in statement.get("subject", []))
                and statement.get("predicate") == expected):
            return output
    raise ValueError("no verified attestation binds the expected predicate to this image digest")


def sign_image(reference, signer, sbom_path, provenance_path):
    run("cosign", "sign", "--yes", reference)
    for predicate_type, path in ((SPDX, sbom_path), (SLSA, provenance_path)):
        run("cosign", "attest", "--yes", "--type", predicate_type, "--predicate", str(path), reference)
    return verify_image(reference, signer, read_json(sbom_path), read_json(provenance_path))


def verify_image(reference, signer, sbom, provenance):
    signature = run("cosign", "verify", *trust_flags(signer), reference)
    require(json.loads(signature), "cosign returned no verified image signatures")
    sbom_evidence = verify_attestation(reference, signer, SPDX, sbom)
    provenance_evidence = verify_attestation(reference, signer, SLSA, provenance)
    return {"signature": signature, "sbom": sbom_evidence, "provenance": provenance_evidence}


def sign_file(path, bundle, signer):
    run("cosign", "sign-blob", "--yes", "--bundle", str(bundle), str(path))
    run("cosign", "verify-blob", *trust_flags(signer), "--bundle", str(bundle), str(path))


def checksums(directory, filename, excluded=()):
    paths = sorted(p for p in directory.iterdir() if p.is_file() and p.name not in {filename, *excluded})
    lines = [f"{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.name}\n" for p in paths]
    (directory / filename).write_text("".join(lines), encoding="utf-8")


def verify_checksums(directory):
    seen = set()
    for line in (directory / "candidate.SHA256SUMS").read_text(encoding="utf-8").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9_.-]+)", line)
        require(match is not None, "invalid candidate checksum entry")
        digest, filename = match.groups()
        require(filename not in seen, "duplicate checksum entry")
        seen.add(filename)
        path = directory / filename
        require(not path.is_symlink() and path.is_file(), "missing or unsafe candidate evidence file")
        require(hashlib.sha256(path.read_bytes()).hexdigest() == digest, f"candidate checksum differs: {filename}")
    expected = {"images.json"} | {f"{n}.{suffix}" for n in NAMES
                                for suffix in ("image.json", "spdx.json", "provenance.json", "verification.json")}
    require(seen == expected, "candidate checksum inventory is incomplete or unexpected")


def api_json(path):
    request = urllib.request.Request("https://api.github.com/" + path,
                                     headers={"Authorization": "Bearer " + os.environ["GH_TOKEN"],
                                              "Accept": "application/vnd.github+json", "User-Agent": "blood-supply-release"})
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        if error.code == 404:
            return None
        raise


def registry_digest(image, tag):
    """Only an authenticated manifest 404 means absent; transport/auth errors fail closed."""
    repository = image.removeprefix("ghcr.io/")
    scope = urllib.parse.urlencode({"service": "ghcr.io", "scope": f"repository:{repository}:pull"})
    credentials = base64.b64encode(f"{os.environ['GITHUB_ACTOR']}:{os.environ['GH_TOKEN']}".encode()).decode()
    request = urllib.request.Request("https://ghcr.io/token?" + scope, headers={"Authorization": "Basic " + credentials})
    with urllib.request.urlopen(request, timeout=30) as response:
        token = json.load(response)["token"]
    request = urllib.request.Request(f"https://ghcr.io/v2/{repository}/manifests/{tag}", method="HEAD",
                                     headers={"Authorization": "Bearer " + token,
                                              "Accept": "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json"})
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            digest = response.headers.get("Docker-Content-Digest", "")
            require(DIGEST.fullmatch(digest), "registry did not return a valid digest")
            return digest
    except urllib.error.HTTPError as error:
        if error.code == 404:
            return None
        raise


def guard_tag(existing, expected):
    require(existing is None or existing == expected, "refusing to overwrite an existing version tag with a different digest")


def validate_k8s_evidence(evidence, manifest_path, repository, commit, version, candidate_run_id, k8s_run):
    require(k8s_run and k8s_run.get("status") == "completed" and k8s_run.get("conclusion") == "success",
            "Kubernetes run must have completed successfully")
    require(k8s_run.get("path") == ".github/workflows/k8s.yml" and k8s_run.get("event") == "workflow_dispatch"
            and k8s_run.get("head_sha") == commit and k8s_run.get("repository", {}).get("full_name") == repository,
            "Kubernetes workflow source or repository differs")
    require(evidence.get("schema_version") == 1, "unsupported Kubernetes evidence schema")
    require(evidence.get("images_run_id") == candidate_run_id and evidence.get("source_commit") == commit
            and evidence.get("candidate_version") == version, "Kubernetes evidence candidate identity differs")
    require(evidence.get("images_manifest_sha256") == hashlib.sha256(manifest_path.read_bytes()).hexdigest(),
            "Kubernetes gate did not test this exact image manifest")
    require(all(evidence.get(gate) is True for gate in ("readiness", "startup", "chaos", "drill")),
            "Kubernetes readiness, startup, chaos, and drill gates must all pass")
    seconds = evidence.get("startup_seconds")
    require(type(seconds) in (float, int) and math.isfinite(seconds) and 0 <= seconds <= 600,
            "cold Kubernetes startup must be finite and at most 600 seconds")


def plan():
    version = os.environ["VERSION"]
    number = version_number(version)
    phase = os.environ["PHASE"]
    require(COMMIT.fullmatch(os.environ["GITHUB_SHA"]), "invalid source commit")
    require(run("git", "rev-parse", "HEAD").strip() == os.environ["GITHUB_SHA"], "checkout differs from workflow commit")
    require(os.environ["GITHUB_EVENT_NAME"] == "workflow_dispatch", "explicit dispatch required")
    if phase == "candidate":
        require(os.environ["GITHUB_REF"] == "refs/heads/main", "candidate dispatch must select main")
    else:
        require(phase == "promote", "unknown release phase")
        require(os.environ["GITHUB_REF"] == "refs/tags/" + version, "promotion must select the matching existing version tag")
        require(re.fullmatch(r"[1-9][0-9]*", os.environ.get("CANDIDATE_RUN_ID", "")), "promotion requires candidate run ID")
        require(re.fullmatch(r"[1-9][0-9]*", os.environ.get("K8S_RUN_ID", "")), "promotion requires Kubernetes run ID")
        require(run("git", "rev-parse", f"refs/tags/{version}^{{commit}}").strip() == os.environ["GITHUB_SHA"], "version tag moved")
    actual = {p.name for p in Path("services").iterdir() if (p / "cmd" / p.name / "main.go").is_file()}
    require(actual == set(SERVICES), "service inventory changed; update release inventory explicitly")
    matrix = [{"name": n, "dockerfile": dockerfile(n), "service": "" if n == "minio" else n} for n in NAMES]
    candidate_tag = f"candidate-{number}-{os.environ['GITHUB_SHA']}-{os.environ['GITHUB_RUN_ID']}-{os.environ['GITHUB_RUN_ATTEMPT']}"
    with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
        output.write("matrix=" + json.dumps(matrix, separators=(",", ":")) + "\n")
        output.write("candidate_tag=" + candidate_tag + "\n")


def publish_image(args):
    version_number(args.version)
    repository, commit = os.environ["GITHUB_REPOSITORY"], os.environ["GITHUB_SHA"]
    require(os.environ["GITHUB_REF"] == "refs/heads/main", "candidate images require main")
    require(args.image == image_name(repository, args.name) and DIGEST.fullmatch(args.digest), "invalid candidate image")
    reference = args.image + "@" + args.digest
    sbom, provenance = inspect_predicates(reference)
    validate_sbom(sbom)
    validate_provenance(provenance, repository, commit, args.name, args.version, os.environ["GITHUB_RUN_ID"])
    args.output.mkdir(parents=True, exist_ok=True)
    sbom_path, provenance_path = args.output / f"{args.name}.spdx.json", args.output / f"{args.name}.provenance.json"
    write_json(sbom_path, sbom)
    write_json(provenance_path, provenance)
    evidence = sign_image(reference, identity(repository, "images", "refs/heads/main"), sbom_path, provenance_path)
    write_json(args.output / f"{args.name}.verification.json", evidence)
    write_json(args.output / f"{args.name}.image.json", {
        "name": args.name, "image": args.image, "digest": args.digest, "version": args.version,
        "source_commit": commit, "source_repository": repository, "candidate_run_id": os.environ["GITHUB_RUN_ID"],
    })


def assemble(args):
    number = version_number(args.version)
    repository, commit = os.environ["GITHUB_REPOSITORY"], os.environ["GITHUB_SHA"]
    images = {}
    for name in NAMES:
        record = read_json(args.output / f"{name}.image.json")
        for key, value in {"name": name, "image": image_name(repository, name), "version": args.version,
                           "source_commit": commit, "source_repository": repository,
                           "candidate_run_id": os.environ["GITHUB_RUN_ID"]}.items():
            require(record.get(key) == value, f"candidate record {name} differs in {key}")
        validate_sbom(read_json(args.output / f"{name}.spdx.json"))
        validate_provenance(read_json(args.output / f"{name}.provenance.json"), repository, commit,
                            name, args.version, os.environ["GITHUB_RUN_ID"])
        images[f"{record['image']}:{number}"] = record["digest"]
    manifest = {"schema_version": 1, "version": args.version, "source_commit": commit,
                "source_repository": repository, "source_ref": "refs/heads/main", "platform": "linux/amd64",
                "candidate_run_id": os.environ["GITHUB_RUN_ID"], "images": images}
    validate_manifest(manifest, repository, commit)
    write_json(args.output / "images.json", manifest)
    checksums(args.output, "candidate.SHA256SUMS", ("candidate.sigstore.json",))
    verify_checksums(args.output)
    sign_file(args.output / "candidate.SHA256SUMS", args.output / "candidate.sigstore.json",
              identity(repository, "release", "refs/heads/main"))


def verify_manifest_images(manifest, repository, commit, directory=None):
    images = validate_manifest(manifest, repository, commit)
    for name in NAMES:
        image = image_name(repository, name)
        digest = images[f"{image}:{version_number(manifest['version'])}"]
        reference = f"{image}@{digest}"
        sbom, provenance = inspect_predicates(reference)
        if directory is not None:
            require(sbom == read_json(directory / f"{name}.spdx.json"), "candidate SPDX differs from image SBOM")
            require(provenance == read_json(directory / f"{name}.provenance.json"), "candidate provenance differs from image provenance")
        validate_sbom(sbom)
        validate_provenance(provenance, repository, commit, name, manifest["version"], manifest["candidate_run_id"])
        verify_image(reference, identity(repository, "images", "refs/heads/main"), sbom, provenance)
        print(f"Verified {reference}")


def promote(args):
    repository, commit = os.environ["GITHUB_REPOSITORY"], os.environ["GITHUB_SHA"]
    require(os.environ["GITHUB_REF"] == "refs/tags/" + args.version, "promotion requires matching version tag")
    candidate_run = api_json(f"repos/{repository}/actions/runs/{args.candidate_run_id}")
    require(candidate_run and candidate_run.get("conclusion") == "success" and candidate_run.get("status") == "completed",
            "candidate run must have completed successfully")
    require(candidate_run.get("path") == ".github/workflows/release.yml" and candidate_run.get("event") == "workflow_dispatch"
            and candidate_run.get("head_branch") == "main" and candidate_run.get("head_sha") == commit
            and candidate_run.get("repository", {}).get("full_name") == repository,
            "candidate run workflow, source, or repository differs")
    require(api_json(f"repos/{repository}/releases/tags/{args.version}") is None, "release already exists; refusing replacement")
    run("cosign", "verify-blob", *trust_flags(identity(repository, "release", "refs/heads/main")),
        "--bundle", str(args.output / "candidate.sigstore.json"), str(args.output / "candidate.SHA256SUMS"))
    verify_checksums(args.output)
    manifest = read_json(args.output / "images.json")
    images = validate_manifest(manifest, repository, commit, args.version, args.candidate_run_id)
    k8s_run = api_json(f"repos/{repository}/actions/runs/{args.k8s_run_id}")
    evidence = read_json(args.k8s_evidence)
    validate_k8s_evidence(evidence, args.output / "images.json", repository, commit, args.version,
                          args.candidate_run_id, k8s_run)
    # Verify every image before any version tag is changed.
    verify_manifest_images(manifest, repository, commit, args.output)
    for full_tag, digest in images.items():
        image, tag = full_tag.rsplit(":", 1)
        guard_tag(registry_digest(image, tag), digest)
    write_json(args.output / "k8s-release-evidence.json", evidence)
    write_json(args.output / "k8s-run.json", {
        "id": str(k8s_run["id"]), "html_url": k8s_run["html_url"], "head_sha": k8s_run["head_sha"],
        "conclusion": k8s_run["conclusion"], "candidate_run_id": args.candidate_run_id,
    })
    signer = identity(repository, "release", "refs/tags/" + args.version)
    for name in NAMES:
        image = image_name(repository, name)
        tag = version_number(args.version)
        digest = images[f"{image}:{tag}"]
        reference = f"{image}@{digest}"
        evidence = sign_image(reference, signer, args.output / f"{name}.spdx.json", args.output / f"{name}.provenance.json")
        write_json(args.output / f"{name}.release-verification.json", evidence)
        existing = registry_digest(image, tag)
        guard_tag(existing, digest)
        if existing is None:
            # Copy a single existing index without changing its manifest bytes.
            run("docker", "buildx", "imagetools", "create", "--prefer-index=false", "--tag", f"{image}:{tag}", reference)
        require(registry_digest(image, tag) == digest, "promoted tag digest differs from tested candidate")
    (args.output / "notes.md").write_text(
        f"Release {args.version}\n\nSource commit: `{commit}`. Platform: `linux/amd64`.\n\n"
        "All 12 service images and the MinIO platform image are pinned in `images.json`. "
        "Promotion preserves the candidate digests. Each image has verified keyless signatures, "
        "an SPDX SBOM, and source-bound BuildKit SLSA v1 provenance. "
        "See `docs/release.md` at this tag for verification and deployment instructions.\n",
        encoding="utf-8")
    checksums(args.output, "SHA256SUMS", ("release.sigstore.json",))
    sign_file(args.output / "SHA256SUMS", args.output / "release.sigstore.json", signer)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("plan")
    for command in ("image", "assemble", "promote"):
        child = commands.add_parser(command)
        child.add_argument("--version", required=True)
        child.add_argument("--output", type=Path, required=True)
        if command == "image":
            child.add_argument("--name", choices=NAMES, required=True)
            child.add_argument("--image", required=True)
            child.add_argument("--digest", required=True)
        elif command == "promote":
            child.add_argument("--candidate-run-id", required=True)
            child.add_argument("--k8s-run-id", required=True)
            child.add_argument("--k8s-evidence", type=Path, required=True)
    child = commands.add_parser("verify-manifest")
    child.add_argument("--manifest", type=Path, required=True)
    child.add_argument("--repository", required=True)
    child.add_argument("--commit", required=True)
    child.add_argument("--online", action="store_true", help="also verify all registry signatures, SBOMs and provenance")
    args = parser.parse_args()
    if args.command == "plan":
        plan()
    elif args.command == "image":
        publish_image(args)
    elif args.command == "assemble":
        assemble(args)
    elif args.command == "promote":
        promote(args)
    else:
        manifest = read_json(args.manifest)
        validate_manifest(manifest, args.repository, args.commit)
        if args.online:
            verify_manifest_images(manifest, args.repository, args.commit)
        print("Manifest verified for " + args.commit)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        print(f"release: {error}", file=sys.stderr)
        sys.exit(1)
