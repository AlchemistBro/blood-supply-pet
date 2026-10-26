"""Bind successful runtime acceptance to one release candidate's exact digests."""
import hashlib
import json
import os
from pathlib import Path
import re


def require(value, message):
    if not value:
        raise ValueError(message)


def load(path):
    return json.loads(Path(path).read_text(encoding='utf8'))


def build(manifest, startup, chaos, application, readiness, hpa, images_run_id, commit, version, digest):
    require(manifest['source_commit'] == commit and manifest['candidate_run_id'] == images_run_id
            and manifest['version'] == version, 'Candidate identity differs')
    require(re.fullmatch('[1-9][0-9]*', images_run_id), 'Invalid candidate run ID')
    seconds = startup.get('elapsed_seconds')
    require(startup.get('mode') == 'release' and startup.get('exit_code') == 0
            and type(seconds) is int and 0 <= seconds <= 600, 'Cold release startup gate failed')
    checks = chaos.get('checks', [])
    require(len(checks) == 4 and {c['name'] for c in checks} == {'gateway', 'kafka', 'keycloak', 'dlt'}
            and all(c['status'] == 'passed' for c in checks) and not chaos.get('cleanup_errors'), 'Chaos gate failed')
    dlt = next(c['evidence'] for c in checks if c['name'] == 'dlt')
    require(dlt.get('mailpit_delivery', {}).get('message_id'), 'Real alert delivery missing')
    for name in ('pre_backup_request_restored', 'encrypted_case_decrypted', 'new_request_in_restored_only',
                 'business_saga_after_restore', 'original_endpoints_restored'):
        require(application.get(name) is True, 'Application recovery gate failed: ' + name)
    require({(x['user'], x['path'], x['status']) for x in application['browser']} == {
        ('analyst1', '/demand', 200), ('physician1', '/requests', 200), ('operator-c001', '/stock', 200)},
        'Restored role acceptance missing')
    deployments = readiness.get('items', [])
    require(len(deployments) == 9 and all(d['status'].get('availableReplicas', 0) >= d['spec'].get('replicas', 1)
                                        for d in deployments), 'Service readiness gate failed')
    require({d['metadata']['name'] for d in hpa['items']} == {'web', 'inventory', 'matching'}
            and all(any(c['type'] == 'ScalingActive' and c['status'] == 'True' for c in d['status'].get('conditions', []))
                    for d in hpa['items']), 'HPA metrics gate failed')
    return {'schema_version': 1, 'images_run_id': images_run_id, 'source_commit': commit,
            'candidate_version': version, 'images_manifest_sha256': digest,
            'readiness': True, 'startup': True, 'startup_seconds': seconds, 'chaos': True, 'drill': True}


def main():
    manifest_path = Path('dist/release/images.json')
    evidence = build(load(manifest_path), load('dist/k8s-startup.json'), load('artifacts/k8s-chaos/report.json'),
                     load('dist/drill-application.json'), load('dist/k8s-readiness.json'), load('dist/k8s-hpa.json'),
                     os.environ['IMAGES_RUN_ID'], os.environ['GITHUB_SHA'], os.environ['CANDIDATE_VERSION'],
                     hashlib.sha256(manifest_path.read_bytes()).hexdigest())
    Path('dist/k8s-release-evidence.json').write_text(json.dumps(evidence, indent=2), encoding='utf8')
    print('Release evidence: all candidate-bound runtime gates passed')


if __name__ == '__main__':
    main()
