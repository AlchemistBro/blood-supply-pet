"""Application acceptance against a PITR copy in the dedicated demo kind cluster."""
import argparse
import base64
import contextlib
import http.cookiejar
import json
import os
from pathlib import Path
import socket
import ssl
import subprocess
import tempfile
import time
from html.parser import HTMLParser
from urllib import error, parse, request
import uuid

K = ['kubectl', '--context', 'kind-blood-supply']
DB_SERVICES = ['inventory', 'demand', 'planning', 'matching', 'transfer',
               'logistics-gateway', 'notification', 'audit']


def kubectl(*args, stdin=None):
    p = subprocess.run(K + list(args), input=stdin, text=True, encoding='utf8',
                       capture_output=True, timeout=660)
    if p.returncode:
        # Never include resolved database URLs, Secrets or credentials in diagnostics.
        raise RuntimeError('kubectl failed: ' + ' '.join(args[:4]))
    return p.stdout


def get(kind, name, namespace):
    return json.loads(kubectl('-n', namespace, 'get', kind, name, '-o', 'json'))


def guard():
    env = get('namespace', 'bs-data', 'default')['metadata']['labels'].get('blood-supply.io/env')
    if env not in ('local', 'ci'):
        raise RuntimeError('Application restore drill requires demo local/ci environment')
    config = json.loads(kubectl('config', 'view', '--minify', '-o', 'json'))
    if parse.urlsplit(config['clusters'][0]['cluster']['server']).hostname not in ('localhost', '127.0.0.1', '::1'):
        raise RuntimeError('Application restore drill requires a loopback kind API')
    nodes = json.loads(kubectl('get', 'nodes', '-o', 'json'))['items']
    if not nodes or not all(n['spec'].get('providerID', '').startswith('kind://') for n in nodes):
        raise RuntimeError('Application restore drill requires kind nodes')


def database_url(raw):
    u = parse.urlsplit(raw)
    if u.scheme not in ('postgres', 'postgresql') or u.hostname != 'pg-rw.bs-data.svc':
        raise ValueError('Unexpected source database endpoint')
    authority, separator, endpoint = u.netloc.rpartition('@')
    if not separator or endpoint != 'pg-rw.bs-data.svc:5432':
        raise ValueError('Unexpected source database authority')
    return parse.urlunsplit(u._replace(netloc=authority + '@pg-drill-rw.bs-data.svc:5432'))


@contextlib.contextmanager
def forward(namespace, service, remote):
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        port = s.getsockname()[1]
    with tempfile.TemporaryFile() as log:
        process = subprocess.Popen(K + ['-n', namespace, 'port-forward', '--address=127.0.0.1',
                                   'service/' + service, f'{port}:{remote}'], stdout=log, stderr=log)
        try:
            for _ in range(100):
                if process.poll() is not None:
                    raise RuntimeError('Port forward stopped: ' + service)
                try:
                    with socket.create_connection(('127.0.0.1', port), timeout=0.5):
                        break
                except OSError:
                    time.sleep(0.2)
            else:
                raise RuntimeError('Port forward not ready: ' + service)
            yield f'http://127.0.0.1:{port}'
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)


def json_http(url, method='GET', token='', body=None, form=None, expected=200):
    headers = {}
    data = None
    if token:
        headers['Authorization'] = 'Bearer ' + token
    if form is not None:
        data = parse.urlencode(form).encode()
        headers['Content-Type'] = 'application/x-www-form-urlencoded'
    elif body is not None:
        data = json.dumps(body).encode()
        headers['Content-Type'] = 'application/json'
    try:
        with request.urlopen(request.Request(url, data, headers, method=method), timeout=20) as r:
            if r.status != expected:
                raise RuntimeError(f'Unexpected HTTP {r.status}: {parse.urlsplit(url).path}')
            raw = r.read(12 * 1024 * 1024)
            return json.loads(raw) if raw else None
    except error.HTTPError as e:
        raise RuntimeError(f'HTTP {e.code}: {parse.urlsplit(url).path}') from None


def login(keycloak, user):
    return json_http(keycloak + '/auth/realms/blood-supply/protocol/openid-connect/token',
                     method='POST', form={'grant_type': 'password', 'client_id': 'blood-supply-cli',
                                          'username': user, 'password': 'demo'})['access_token']


class LoginForm(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.active = False
        self.action = None
        self.fields = {}

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == 'form' and attrs.get('id') == 'kc-form-login':
            self.active = True
            self.action = attrs.get('action')
        elif self.active and tag == 'input' and attrs.get('name'):
            self.fields[attrs['name']] = attrs.get('value', '')

    def handle_endtag(self, tag):
        if tag == 'form':
            self.active = False


def browser_login(web, ca, user, page):
    context = ssl.create_default_context(cafile=ca)
    opener = request.build_opener(request.HTTPCookieProcessor(http.cookiejar.CookieJar()),
                                 request.HTTPSHandler(context=context))
    url = web + '/auth/login?' + parse.urlencode({'hint': user, 'next': page})
    with opener.open(url, timeout=30) as r:
        form = LoginForm()
        form.feed(r.read(2 * 1024 * 1024).decode('utf8'))
        origin = r.url
    if form.action is None:
        raise RuntimeError('Keycloak login form missing for ' + user)
    form.fields.update(username=user, password='demo')
    with opener.open(request.Request(parse.urljoin(origin, form.action),
                                    parse.urlencode(form.fields).encode()), timeout=30) as r:
        destination = parse.urlsplit(r.url)
        expected = parse.urlsplit(web)
        body = r.read(4 * 1024 * 1024).decode('utf8')
        if (r.status != 200 or destination.scheme != expected.scheme or
                destination.netloc != expected.netloc or destination.path != page or 'Выйти' not in body):
            raise RuntimeError('OIDC did not open expected role page for ' + user)
    return {'user': user, 'path': page, 'status': 200}


def new_request(matching, token, key):
    return json_http(matching + '/requests', method='POST', token=token, expected=201, body={
        'idempotency_key': key, 'patient_case_no': 'PITR-' + key,
        'component_kind': 'RBC', 'requested_units': 1, 'urgency': 'PLANNED',
        'blood_group': 'O+', 'phenotype': '', 'constraints': []})


def prepare(state_path):
    guard()
    with forward('bs-app', 'matching', 8104) as matching, forward('bs-edge', 'keycloak', 8080) as kc:
        token = login(kc, 'physician1')
        key = str(uuid.uuid4())
        row = new_request(matching, token, key)
        state = {'id': row['clinical_request_id'], 'key': key,
                 'clinic_id': row['clinic_id'], 'requested_units': row['requested_units']}
        Path(state_path).parent.mkdir(parents=True, exist_ok=True)
        Path(state_path).write_text(json.dumps(state), encoding='utf8')
    print('PITR application canary prepared: ' + state['id'])


def policy(namespace, name, selector, direction, peers):
    rule = {'ports': [{'protocol': 'TCP', 'port': 5432}],
            'from' if direction == 'Ingress' else 'to': peers}
    return {'apiVersion': 'networking.k8s.io/v1', 'kind': 'NetworkPolicy',
            'metadata': {'namespace': namespace, 'name': name,
                         'labels': {'blood-supply.io/drill': 'application'}},
            'spec': {'podSelector': selector, 'policyTypes': [direction],
                     direction.lower(): [rule]}}


def restore_policies():
    apps = {'namespaceSelector': {'matchLabels': {'kubernetes.io/metadata.name': 'bs-app'}},
            'podSelector': {'matchExpressions': [{'key': 'app.kubernetes.io/name',
                                                'operator': 'In', 'values': DB_SERVICES}]}}
    kc = {'namespaceSelector': {'matchLabels': {'kubernetes.io/metadata.name': 'bs-edge'}},
          'podSelector': {'matchLabels': {'app.kubernetes.io/name': 'keycloak'}}}
    db = {'namespaceSelector': {'matchLabels': {'kubernetes.io/metadata.name': 'bs-data'}},
          'podSelector': {'matchLabels': {'cnpg.io/cluster': 'pg-drill'}}}
    return [policy('bs-data', 'drill-app-ingress', db['podSelector'], 'Ingress', [apps, kc]),
            policy('bs-app', 'drill-app-egress', apps['podSelector'], 'Egress', [db]),
            policy('bs-edge', 'drill-keycloak-egress', kc['podSelector'], 'Egress', [db])]


def set_env(namespace, name, env):
    patch = [{'op': 'replace', 'path': '/spec/template/spec/containers/0/env', 'value': env}]
    with tempfile.NamedTemporaryFile(mode='w', encoding='utf8', suffix='.json', delete=False) as f:
        json.dump(patch, f)
        filename = f.name
    try:
        kubectl('-n', namespace, 'patch', 'deployment', name, '--type=json', '--patch-file', filename)
    finally:
        os.unlink(filename)


def rollout():
    kubectl('-n', 'bs-edge', 'rollout', 'status', 'deployment/keycloak', '--timeout=600s')
    for name in DB_SERVICES:
        kubectl('-n', 'bs-app', 'rollout', 'status', 'deployment/' + name, '--timeout=300s')
    kubectl('-n', 'bs-app', 'rollout', 'restart', 'deployment/web')
    kubectl('-n', 'bs-app', 'rollout', 'status', 'deployment/web', '--timeout=300s')


def count(cluster, request_id):
    request_id = str(uuid.UUID(request_id))
    pod = get('cluster', cluster, 'bs-data')['status']['currentPrimary']
    return kubectl('-n', 'bs-data', 'exec', pod, '-c', 'postgres', '--', 'psql', '-XAt',
                   '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'matching', '-c',
                   f"SELECT count(*) FROM clinical_request WHERE clinical_request_id='{request_id}'").strip()


def verify(state_path, report_path, web):
    guard()
    state = json.loads(Path(state_path).read_text(encoding='utf8'))
    originals = []
    policies = restore_policies()
    result = {}
    try:
        kubectl('apply', '-f', '-', stdin=json.dumps({'apiVersion': 'v1', 'kind': 'List', 'items': policies}))
        for namespace, name in [('bs-app', s) for s in DB_SERVICES] + [('bs-edge', 'keycloak')]:
            deploy = get('deployment', name, namespace)
            original = deploy['spec']['template']['spec']['containers'][0]['env']
            updated = json.loads(json.dumps(original))
            var = 'KC_DB_URL' if name == 'keycloak' else 'BS_DB_URL'
            for entry in updated:
                if entry['name'] != var:
                    continue
                if name == 'keycloak':
                    expected = 'jdbc:postgresql://pg-rw.bs-data.svc:5432/keycloak'
                    if entry.get('value') != expected:
                        raise RuntimeError('Unexpected Keycloak database endpoint')
                    value = expected.replace('pg-rw.', 'pg-drill-rw.', 1)
                else:
                    ref = entry['valueFrom']['secretKeyRef']
                    secret = get('secret', ref['name'], namespace)
                    value = database_url(base64.b64decode(secret['data'][ref['key']]).decode())
                entry.clear()
                entry.update(name=var, value=value)
                break
            else:
                raise RuntimeError('Database environment entry not found: ' + name)
            originals.append((namespace, name, original))
            set_env(namespace, name, updated)
        rollout()
        print('Restore application: all database clients connected to pg-drill', flush=True)
        with forward('bs-app', 'matching', 8104) as matching, forward('bs-edge', 'keycloak', 8080) as kc:
            token = login(kc, 'physician1')
            row = json_http(matching + '/requests/' + state['id'], token=token)
            if row['clinic_id'] != state['clinic_id'] or row['requested_units'] != state['requested_units']:
                raise RuntimeError('Restored clinical request differs from saved canary')
            case = json_http(matching + '/requests/' + state['id'] + '/patient-case', token=token)
            if case['patient_case_no'] != 'PITR-' + state['key']:
                raise RuntimeError('Restored encrypted patient case did not decrypt')
            created = new_request(matching, token, str(uuid.uuid4()))
            new_id = created['clinical_request_id']
            if count('pg-drill', new_id) != '1' or count('pg', new_id) != '0':
                raise RuntimeError('New request did not write exclusively into restored database')
            result = {'pre_backup_request_restored': True, 'encrypted_case_decrypted': True,
                      'new_request_in_restored_only': True, 'db_services': DB_SERVICES + ['keycloak']}
            print('Restore application: canary, decryption and exclusive new write passed', flush=True)
        for target in ['web:8080', 'inventory:8101', 'demand:8102', 'planning:8103', 'matching:8104',
                       'transfer:8105', 'logistics-gateway:8106', 'notification:8107', 'audit:8108']:
            name, port = target.split(':')
            kubectl('-n', 'bs-app', 'exec', 'deployment/' + name, '--',
                    '/healthcheck', f'http://127.0.0.1:{port}/readyz')
        cert = base64.b64decode(get('secret', 'bs-tls', 'bs-app')['data']['tls.crt'])
        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(cert)
            ca = f.name
        try:
            result['browser'] = []
            for user, page in [('analyst1', '/demand'), ('physician1', '/requests'), ('operator-c001', '/stock')]:
                result['browser'].append(browser_login(web, ca, user, page))
                print('Restore application: OIDC role passed: ' + user, flush=True)
                time.sleep(1)
        finally:
            os.unlink(ca)
        subprocess.run(['python3', 'tools/chaos/run.py', '--scenario', 'saga', '--cluster', 'pg-drill',
                        '--report', 'dist/drill-saga.json'], check=True,
                       env={**os.environ, 'BS_CHAOS_EPHEMERAL': '1'}, timeout=420)
        result['business_saga_after_restore'] = True
    finally:
        # Always return the original demo stand to its original database endpoints.
        cleanup_errors = []
        for namespace, name, env in reversed(originals):
            try:
                set_env(namespace, name, env)
            except Exception:
                cleanup_errors.append(namespace + '/' + name)
        if originals:
            try:
                rollout()
            except Exception:
                cleanup_errors.append('original deployment readiness')
        for obj in policies:
            try:
                kubectl('-n', obj['metadata']['namespace'], 'delete', 'networkpolicy',
                        obj['metadata']['name'], '--ignore-not-found')
            except Exception:
                cleanup_errors.append(obj['metadata']['name'])
        if cleanup_errors:
            raise RuntimeError('Restore drill cleanup failed: ' + ', '.join(cleanup_errors))
    result['original_endpoints_restored'] = True
    Path(report_path).parent.mkdir(parents=True, exist_ok=True)
    Path(report_path).write_text(json.dumps(result, indent=2), encoding='utf8')
    print('PITR application acceptance passed: canary, decryption, new write, 3 OIDC roles, 9 readiness probes')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('phase', choices=['prepare', 'verify'])
    parser.add_argument('--state', default='dist/drill-application-state.json')
    parser.add_argument('--report', default='dist/drill-application.json')
    parser.add_argument('--web', default='https://localhost')
    args = parser.parse_args()
    if args.phase == 'prepare':
        prepare(args.state)
    else:
        verify(args.state, args.report, args.web)


if __name__ == '__main__':
    main()
