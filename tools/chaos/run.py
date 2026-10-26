#!/usr/bin/env python3
"""Real kind failure checks. Standard library only; never starts a cluster.

Host HTTP probes use Kubernetes port-forwards, as the existing kind smoke does.
Application calls and Kafka injection still use the unchanged NetworkPolicies.
"""
import argparse
import base64
import contextlib
import json
import math
import os
from pathlib import Path
import re
import signal
import ssl
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


CONTEXT = "kind-blood-supply"
RULE_UID = "bs-dlt-not-empty"
TOPIC = "logistics.commands"
SCENARIOS = ("gateway", "kafka", "keycloak", "dlt")


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def eventually(what, check, timeout=180, interval=2):
    deadline = time.monotonic() + timeout
    last = None
    while True:
        last = check()  # Schema/auth/transport errors are failures, not success.
        if last:
            return last
        if time.monotonic() >= deadline:
            raise RuntimeError(f"Timed out: {what}; last result: {last!r}")
        time.sleep(interval)


def vector_total(response):
    require(response.get("status") == "success", "Prometheus query failed")
    data = response["data"]
    require(data["resultType"] == "vector", "Expected a Prometheus instant vector")
    values = [float(item["value"][1]) for item in data["result"]]
    require(all(math.isfinite(v) and v >= 0 for v in values), "Invalid metric value")
    return sum(values) if values else None  # Missing is NOT zero or passed.


def firing_instance(response, uid, typ, since):
    require(response.get("status") == "success", "Grafana rules API failed")
    for group in response["data"]["groups"]:
        for rule in group.get("rules", []):
            # Grafana's Prometheus-compatible API includes the provisioned UID
            # in rule.uid (some versions additionally expose it in labels).
            rule_uid = rule.get("uid") or rule.get("labels", {}).get("__alert_rule_uid__")
            if rule_uid != uid:
                continue
            require(rule.get("health") == "ok" and not rule.get("lastError"), f"Grafana rule health: {rule.get('health')}")
            if rule.get("state") != "firing":
                continue
            for alert in rule.get("alerts", []):
                labels = alert.get("labels", {})
                if (alert.get("state", "").lower() in ("firing", "alerting") and labels.get("type") == typ
                        and labels.get("topic") == TOPIC):
                    # A unique type proves this run caused this instance; a
                    # timestamp also rules out old/cached firing evidence.
                    active = alert.get("activeAt", "")
                    from datetime import datetime
                    stamp = datetime.fromisoformat(active.replace("Z", "+00:00")).timestamp()
                    if stamp >= since - 5:
                        return alert
    return None


def injector_job(name, marker, typ):
    return {
        "apiVersion": "batch/v1", "kind": "Job",
        "metadata": {"name": name, "namespace": "bs-app"},
        "spec": {"backoffLimit": 0, "activeDeadlineSeconds": 180,
                 "ttlSecondsAfterFinished": 600,
                 "template": {
                     "metadata": {"labels": {"app.kubernetes.io/name": "kafka-topics",
                                               "blood-supply.io/chaos": "true"}},
                     "spec": {"restartPolicy": "Never", "automountServiceAccountToken": False,
                              "enableServiceLinks": False,
                              "securityContext": {"runAsNonRoot": True, "runAsUser": 65532,
                                                  "runAsGroup": 65532,
                                                  "seccompProfile": {"type": "RuntimeDefault"}},
                              "containers": [{"name": "inject", "image": "blood-supply-chaos:local",
                                              "imagePullPolicy": "Never",
                                              "env": [{"name": "CHAOS_MARKER", "value": marker},
                                                      {"name": "CHAOS_TYPE", "value": typ}],
                                              "securityContext": {"allowPrivilegeEscalation": False,
                                                                  "readOnlyRootFilesystem": True,
                                                                  "capabilities": {"drop": ["ALL"]}},
                                              "resources": {"requests": {"cpu": "10m", "memory": "24Mi"},
                                                            "limits": {"memory": "64Mi"}}}]}}}}


class Stand:
    def __init__(self, args):
        self.args = args
        self.report = {"context": CONTEXT, "cluster": args.cluster, "checks": [], "cleanup_errors": []}
        self.stack = contextlib.ExitStack()
        self.urls = {}
        self.pfs = {}
        self.tokens = {}

    def kubectl(self, *args, stdin=None, timeout=45):
        result = subprocess.run(["kubectl", "--context", CONTEXT, "--request-timeout=30s", *args],
                                input=stdin, text=True, encoding="utf-8", capture_output=True, timeout=timeout)
        # Never include command arguments/stdin/stdout in a failed command:
        # secret reads and token requests must not leak into CI artifacts.
        require(result.returncode == 0, f"kubectl {args[0] if args else ''} failed: {result.stderr[-1500:]}")
        return result.stdout.strip()

    def obj(self, namespace, resource, name=None):
        parts = ["-n", namespace, "get", resource]
        if name:
            parts.append(name)
        return json.loads(self.kubectl(*parts, "-o", "json"))

    def preflight(self):
        require(os.environ.get("BS_CHAOS_EPHEMERAL") == "1",
                "Set BS_CHAOS_EPHEMERAL=1 only for the disposable local/ci kind stand")
        require(re.fullmatch(r"pg(?:-[a-z0-9]+)*", self.args.cluster), "Invalid CNPG test cluster name")
        config = json.loads(self.kubectl("config", "view", "--minify", "-o", "json"))
        server = urllib.parse.urlparse(config["clusters"][0]["cluster"]["server"])
        require(server.hostname in ("127.0.0.1", "localhost", "::1"), "kind API must be loopback")
        nodes = json.loads(self.kubectl("get", "nodes", "-o", "json"))["items"]
        require(nodes and all(n["spec"].get("providerID", "").startswith("kind://") for n in nodes),
                "Only a kind cluster is supported")
        for ns in ("bs-app", "bs-data", "bs-sims", "bs-edge", "bs-observability"):
            doc = self.obj(ns, "namespace", ns)
            require(doc["metadata"].get("labels", {}).get("blood-supply.io/env") in ("local", "ci"),
                    f"{ns} is not labeled local/ci")
        cluster = self.obj("bs-data", "cluster.postgresql.cnpg.io", self.args.cluster)
        for source in cluster["spec"].get("externalClusters", []):
            # PITR names its in-cluster MinIO archive an "externalCluster".
            # Permit that archive, never an external database connection.
            archive = source.get("barmanObjectStore", {})
            require("connectionParameters" not in source and archive.get("endpointURL") == "http://minio.bs-data.svc:9000",
                    "Only the in-cluster MinIO recovery archive is supported")
        require(cluster["spec"].get("enableSuperuserAccess") is False, "Remote superuser must remain disabled")
        for service in ("inventory", "transfer", "logistics"):
            self.database(service)
        for service, env_name, expected in (("inventory", "BS_AIST_URL", "http://sim-aist.bs-sims.svc:8201"),
                                             ("logistics-gateway", "BS_C03_URL", "http://sim-logistics.bs-sims.svc:8203")):
            deployment = self.obj("bs-app", "deployment", service)
            env = deployment["spec"]["template"]["spec"]["containers"][0]["env"]
            ref = next(e for e in env if e["name"] == env_name)["valueFrom"]["configMapKeyRef"]
            require(self.obj("bs-app", "configmap", ref["name"])["data"][ref["key"]] == expected,
                    f"{service} must target the local simulator")
        self.obj("bs-sims", "deployment", "sim-logistics")
        self.obj("bs-sims", "deployment", "sim-aist")

    def request(self, url, method="GET", body=None, token=None, form=None, basic=None):
        headers = {}
        data = None
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        if form is not None:
            data = urllib.parse.urlencode(form).encode()
            headers["Content-Type"] = "application/x-www-form-urlencoded"
        if token:
            headers["Authorization"] = "Bearer " + token
        if basic:
            headers["Authorization"] = "Basic " + base64.b64encode(basic.encode()).decode()
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            tls = None
            if url.startswith("https:"):
                cert = self.obj("bs-app", "secret", "bs-tls")["data"]["tls.crt"]
                tls = ssl.create_default_context(cadata=base64.b64decode(cert).decode())
            resp = urllib.request.urlopen(req, timeout=12, context=tls)
        except urllib.error.HTTPError as exc:
            resp = exc
        with resp:
            raw = resp.read(2 << 20)
            try:
                value = json.loads(raw)
            except (ValueError, UnicodeDecodeError):
                value = None
            return resp.status, value

    def json(self, url, **kwargs):
        status, value = self.request(url, **kwargs)
        require(status == 200 and value is not None, f"Expected JSON HTTP 200 from {urllib.parse.urlparse(url).path}: {status}")
        return value

    def forward(self, key, namespace, target, port, replace=False):
        if key in self.urls and not replace:
            require(self.pfs[key].poll() is None, f"Port-forward {key} died")
            return self.urls[key]
        if key in self.pfs:
            self.stop_forward(self.pfs[key])
        log = self.stack.enter_context(tempfile.TemporaryFile(mode="w+t", encoding="utf-8"))
        proc = subprocess.Popen(["kubectl", "--context", CONTEXT, "-n", namespace,
                                 "port-forward", "--address=127.0.0.1", target, f":{port}"],
                                stdout=log, stderr=subprocess.STDOUT, text=True)
        self.stack.callback(self.stop_forward, proc)
        self.pfs[key] = proc

        def ready():
            require(proc.poll() is None, f"Port-forward {key} exited")
            log.seek(0)
            match = re.search(r"Forwarding from 127\.0\.0\.1:(\d+)", log.read())
            return match.group(1) if match else None

        local_port = eventually(f"port-forward {key}", ready, timeout=30, interval=.25)
        self.urls[key] = f"http://127.0.0.1:{local_port}"
        return self.urls[key]

    @staticmethod
    def stop_forward(proc):
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait(timeout=5)

    def endpoint(self, service):
        ns, port = {"transfer": ("bs-app", 8105), "inventory": ("bs-app", 8101),
                    "matching": ("bs-app", 8104), "gateway": ("bs-app", 8106),
                    "sim-aist": ("bs-sims", 8201), "sim-logistics": ("bs-sims", 8203),
                    "keycloak": ("bs-edge", 8080), "grafana": ("bs-observability", 3000),
                    "mailpit": ("bs-sims", 8025)}[service]
        name = {"gateway": "logistics-gateway", "grafana": "otel-lgtm"}.get(service, service)
        return self.forward(service, ns, "service/" + name, port)

    def login(self, user=None):
        form = {"grant_type": "password", "client_id": "blood-supply-cli",
                "username": user, "password": "demo"}
        if user is None:
            secret = self.obj("bs-app", "secret", "service-client")["data"]
            form = {"grant_type": "client_credentials",
                    "client_id": base64.b64decode(secret["BS_SERVICE_CLIENT_ID"]).decode(),
                    "client_secret": base64.b64decode(secret["BS_SERVICE_CLIENT_SECRET"]).decode()}
        value = self.json(self.endpoint("keycloak") + "/auth/realms/blood-supply/protocol/openid-connect/token",
                          method="POST", form=form)
        require(value.get("access_token"), "Keycloak did not issue a token")
        return value["access_token"]

    def database(self, service):
        # Resolve the secret from the current Deployment rather than assuming
        # the pre-PITR secret name survived the parent's reroute.
        deployment_name = "logistics-gateway" if service == "logistics" else service
        env = self.obj("bs-app", "deployment", deployment_name)["spec"]["template"]["spec"]["containers"][0]["env"]
        entry = next(e for e in env if e["name"] == "BS_DB_URL")
        if "value" in entry:
            # Parent PITR acceptance temporarily replaces the env value, then
            # restores the original secret reference after its verification.
            raw = entry["value"]
        else:
            ref = entry["valueFrom"]["secretKeyRef"]
            encoded = self.obj("bs-app", "secret", ref["name"])["data"][ref["key"]]
            raw = base64.b64decode(encoded).decode()
        db = urllib.parse.urlparse(raw)
        require(db.hostname in (f"{self.args.cluster}-rw.bs-data.svc", f"{self.args.cluster}-rw.bs-data.svc.cluster.local"),
                f"{service} DB has not been rerouted to {self.args.cluster}")
        require(db.username == service and db.path == "/" + service, f"Unexpected DB role/database for {service}")
        return db

    def sql(self, service, query):
        # Use only the deployed role inside a read-only local transaction.
        self.database(service)
        pods = json.loads(self.kubectl("-n", "bs-data", "get", "pods", "-l",
                                     f"cnpg.io/cluster={self.args.cluster},cnpg.io/instanceRole=primary", "-o", "json"))["items"]
        require(len(pods) == 1, "Expected exactly one CNPG primary")
        statement = f"BEGIN READ ONLY; SET LOCAL ROLE {service}; {query}; COMMIT;"
        return self.kubectl("-n", "bs-data", "exec", pods[0]["metadata"]["name"], "-c", "postgres", "--",
                            "psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", service,
                            "-c", statement)

    def pods(self, namespace, name):
        return json.loads(self.kubectl("-n", namespace, "get", "pods", "-l",
                                     "app.kubernetes.io/name=" + name, "-o", "json"))["items"]

    @contextlib.contextmanager
    def outage(self, namespace, kind, name):
        before = self.obj(namespace, kind, name)
        count = before["spec"]["replicas"]
        require(count > 0, f"{name} was already stopped")
        require(before["status"].get("readyReplicas", 0) == count, f"{name} was not healthy before fault")
        uid = before["metadata"]["uid"]
        try:
            self.kubectl("-n", namespace, "scale", f"{kind}/{name}", "--replicas=0",
                         f"--current-replicas={count}", f"--resource-version={before['metadata']['resourceVersion']}")
            eventually(f"all {name} pods stopped", lambda: not self.pods(namespace, name), timeout=180)
            yield
        finally:
            try:
                current = self.obj(namespace, kind, name)
                if current["spec"]["replicas"] != count or current["metadata"]["uid"] != uid:
                    require(current["metadata"]["uid"] == uid, f"{name} was replaced concurrently; refusing to scale it")
                    require(current["spec"]["replicas"] == 0, f"{name} replicas changed concurrently")
                    self.kubectl("-n", namespace, "scale", f"{kind}/{name}", f"--replicas={count}", "--current-replicas=0")
                    self.kubectl("-n", namespace, "rollout", "status", f"{kind}/{name}", "--timeout=240s", timeout=250)
            except Exception as exc:
                self.report["cleanup_errors"].append(f"Restore {name}: {exc}")
                raise

    def prepare(self):
        self.tokens = {"service": self.login(), "operator": self.login("operator-c001")}
        marker = "CHAOS-" + uuid.uuid4().hex[:20]
        status, _ = self.request(self.endpoint("sim-aist") + "/dev/units", method="POST", body={
            "aist_unit_code": marker, "clinic_code": "C001", "component_kind": "RBC",
            "blood_group": "O+", "storage_location": marker})
        require(status == 201, f"Cannot create fresh simulator unit: HTTP {status}")
        operator = self.tokens["operator"]

        def find_unit():
            offset = 0
            while True:
                page = self.json(self.endpoint("inventory") + f"/clinics/1/units?kind=RBC&limit=1000&offset={offset}", token=operator)
                for unit in page["units"]:
                    if unit["storage_location"] == marker and unit["available"]:
                        return str(uuid.UUID(unit["unit_id"]))
                if not page["has_more"]:
                    return None
                offset += 1000

        unit_id = eventually("fresh unit in inventory HTTP projection", find_unit)
        obligation = self.json(self.endpoint("transfer") + "/obligations", method="POST", token=self.tokens["service"], body={
            "choice_id": str(uuid.uuid4()), "request_id": str(uuid.uuid4()), "origin": "DOCTOR_CHOICE",
            "source_clinic_id": 1, "target_clinic_id": 2, "component_kind": "RBC", "requested_qty": 1,
            "urgency": "PLANNED", "confirm_window_seconds": 3600, "spec": {"rule_version": "chaos-kind"}})
        require(obligation["status"] == "PENDING", "New obligation is not PENDING")
        oid = str(uuid.UUID(obligation["obligation_id"]))
        eventually("fresh unit in transfer HTTP projection", lambda: any(
            str(u["unit_id"]) == unit_id for u in self.json(self.endpoint("transfer") + f"/obligations/{oid}/candidates",
                                                           token=operator)["units"]))
        case = {"obligation_id": oid, "unit_id": unit_id, "idempotency_key": str(uuid.uuid4())}
        self.report.setdefault("business_cases", []).append(case)
        return case

    def confirm(self, case):
        result = self.json(self.endpoint("transfer") + f"/obligations/{case['obligation_id']}/confirm", method="POST",
                           token=self.tokens["operator"], body={"unit_ids": [case["unit_id"]], "idempotency_key": case["idempotency_key"]})
        require(result.get("code") == "CONFIRMED", f"Confirmation failed: {result.get('code')}")

    def shipments(self, case):
        return self.json(self.endpoint("sim-logistics") + "/dev/shipments?clientRef=o07-" + case["obligation_id"])["shipments"]

    def recovered(self, case):
        oid = case["obligation_id"]

        def accepted():
            value = self.json(self.endpoint("transfer") + f"/obligations/{oid}", token=self.tokens["service"])
            return value if value["status"] == "TRANSPORT_REQUESTED" and value.get("transport_ref") else None

        obligation = eventually("business saga TRANSPORT_REQUESTED with carrier reference", accepted, timeout=240)
        self.confirm(case)  # Replay the same business request after recovery.
        # Observe beyond the gateway lease and another reconciler pass; checking
        # n==1 at a single instant would miss a delayed duplicate.
        deadline = time.monotonic() + self.args.stability_seconds
        while True:
            rows = self.shipments(case)
            require(len(rows) == 1 and not rows[0]["cancelled"], f"Expected exactly one live carrier order, found {len(rows)}")
            require(rows[0]["shipmentRef"] == obligation["transport_ref"], "Saga and carrier reference differ")
            require(rows[0]["containers"] == 1 and rows[0]["sourceClinicId"] == 1 and rows[0]["targetClinicId"] == 2,
                    "Carrier order does not match confirmed business request")
            if time.monotonic() >= deadline:
                break
            time.sleep(2)
        return case

    def saga(self):
        case = self.prepare()
        self.confirm(case)
        return self.recovered(case)

    def gateway(self):
        case = self.prepare()
        old_uids = {p["metadata"]["uid"] for p in self.pods("bs-app", "logistics-gateway")}
        with self.outage("bs-app", "deployment", "logistics-gateway"):
            self.confirm(case)
            require(not self.shipments(case), "Carrier order created while gateway is stopped")
            oid = case["obligation_id"]
            require(self.sql("transfer", f"SELECT count(*) FROM allocation WHERE obligation_id='{oid}' AND status='CONFIRMED'") == "1",
                    "Confirmation was not durably allocated")
            eventually("transport command published while gateway is stopped", lambda: self.sql(
                "transfer", f"SELECT count(*) FROM outbox WHERE message_type='RequestTransport' AND key='{oid}' AND published_at IS NOT NULL") == "1")
        require(old_uids.isdisjoint({p["metadata"]["uid"] for p in self.pods("bs-app", "logistics-gateway")}),
                "Gateway pods were not replaced")
        return self.recovered(case)

    def kafka(self):
        case = self.prepare()
        # Bind to a live pod directly: Service forwarding after it becomes
        # unready would hide the actual /readyz response.
        self.endpoint("gateway")
        with self.outage("bs-app", "statefulset", "kafka"):
            code, health = self.request(self.endpoint("gateway") + "/readyz")
            require(code == 503 and health and any(c["name"] == "kafka" and not c["ok"] for c in health["checks"]),
                    "Gateway readiness did not identify the Kafka outage")
            require(self.request(self.endpoint("gateway") + "/healthz")[0] == 200, "Kafka outage broke process liveness")
            self.confirm(case)
            oid = case["obligation_id"]
            # Confirm succeeds over HTTP even though the command cannot leave
            # the transactional outbox. Do not mistake an HTTP 200 for durable success.
            for _ in range(max(1, self.args.outage_seconds // 5)):
                require(self.sql("transfer", f"SELECT count(*) FROM outbox WHERE message_type='RequestTransport' AND key='{oid}' AND published_at IS NULL") == "1",
                        "Transport command not durable/pending during Kafka outage")
                require(not self.shipments(case), "Carrier order created while Kafka is stopped")
                time.sleep(5)
        eventually("Gateway readiness after Kafka recovery", lambda: self.request(self.endpoint("gateway") + "/readyz")[0] == 200)
        self.recovered(case)
        require(self.sql("transfer", f"SELECT count(*) FROM outbox WHERE message_type='RequestTransport' AND key='{case['obligation_id']}' AND published_at IS NULL") == "0",
                "Outbox did not drain after Kafka recovered")
        return case

    def keycloak(self):
        token = self.login("physician1")
        endpoint = self.endpoint("matching") + "/requests"
        self.json(endpoint, token=token)  # Warm this pod's JWKS cache.
        token_path = "/auth/realms/blood-supply/protocol/openid-connect/token"
        with self.outage("bs-edge", "deployment", "keycloak"):
            self.json(endpoint, token=token)
            require(self.request(endpoint)[0] == 401, "Unauthenticated request accepted during IdP outage")
            require(self.request(endpoint, token="invalid.jwt.token")[0] == 401, "Invalid JWT accepted during IdP outage")
            # A dead direct Keycloak port-forward is not login evidence. Use
            # the still-live ingress controller to attempt a real token grant.
            ingress = self.forward("ingress", "ingress-nginx", "service/ingress-nginx-controller", 443)
            ingress = ingress.replace("http://127.0.0.1:", "https://localhost:")
            status, value = self.request(ingress + token_path, method="POST", form={
                "grant_type": "password", "client_id": "blood-supply-cli", "username": "physician1", "password": "demo"})
            require(status in (502, 503, 504) and not (value and value.get("access_token")),
                    f"New login did not fail through ingress as expected: HTTP {status}")
        self.urls.pop("keycloak", None)
        fresh = self.login("physician1")
        self.json(endpoint, token=fresh)
        return {"cached_access_token": "accepted", "new_token_during_outage": "rejected", "new_token_after_recovery": "accepted"}

    def grafana(self, path):
        basic = os.environ.get("BS_CHAOS_GRAFANA_BASIC", "admin:admin")
        return self.json(self.endpoint("grafana") + path, basic=basic)

    def metric(self, typ):
        selector = json.dumps(typ)
        expr = f'sum(messaging_consumer_dead_letters_total{{topic="{TOPIC}",type={selector}}})'
        payload = self.grafana("/api/datasources/proxy/uid/prometheus/api/v1/query?" + urllib.parse.urlencode({"query": expr}))
        return vector_total(payload)

    def inject(self, typ):
        marker = uuid.uuid4().hex
        name = "chaos-dlt-" + marker[:12]
        try:
            self.kubectl("create", "-f", "-", stdin=json.dumps(injector_job(name, marker, typ)))
            # Poll conditions to fail immediately on failed jobs, not after the
            # complete timeout, and retain only non-sensitive injector logs.
            def complete():
                job = self.obj("bs-app", "job", name)
                require(not job["status"].get("failed"), f"DLT injector {name} failed")
                return job["status"].get("succeeded", 0) == 1
            eventually("exact poison record in real Kafka DLT", complete, timeout=200)
            logs = self.kubectl("-n", "bs-app", "logs", "job/" + name)
            evidence = json.loads(logs)
            require(evidence.get("dlt_verified") is True and evidence.get("marker") == marker and evidence.get("type") == typ,
                    "Injector did not prove this exact poison reached DLT")
            self.report.setdefault("dlt_records", []).append(evidence)
            return evidence
        finally:
            self.kubectl("-n", "bs-app", "delete", "job", name, "--ignore-not-found", "--wait=true", "--timeout=45s", timeout=55)

    def dlt(self):
        typ = "ChaosPoison_" + uuid.uuid4().hex
        # Check the actual provisioned rule, never install a replacement rule.
        rule = self.grafana("/api/v1/provisioning/alert-rules/" + RULE_UID)
        require(rule.get("uid") == RULE_UID and rule.get("provenance") == "file", "DLT alert is not the file-provisioned rule")
        query = next(d for d in rule["data"] if d["refId"] == "A")
        require(query["datasourceUid"] == "prometheus" and query["model"]["expr"] ==
                "sum by (topic, type) (increase(messaging_consumer_dead_letters_total[10m]))", "Provisioned DLT rule expression differs")
        require(self.metric(typ) is None, "DLT run type is not unique")
        since = time.time()
        self.inject(typ)
        # increase() cannot detect the first sample at 1. Establish a real
        # observed baseline before the second poison; no synthetic zero series.
        baseline = eventually("real DLT counter in Prometheus; wire ConsumerMetrics.DeadLetter into OTLP if absent",
                              lambda: self.metric(typ), timeout=90)
        require(baseline >= 1, "DLT baseline counter is invalid")
        time.sleep(20)  # At least one 15s OTLP export interval.
        self.inject(typ)
        eventually("DLT counter increased after second verified poison", lambda: (self.metric(typ) or 0) >= baseline + 1, timeout=90)
        case = self.saga()  # A valid command AFTER poison reaches C03 and transfer.
        alert = eventually("provisioned DLT alert firing for this exact topic/type", lambda: firing_instance(
            self.grafana("/api/prometheus/grafana/api/v1/rules"), RULE_UID, typ, since), timeout=180)
        def delivered():
            messages = self.json(self.endpoint('mailpit') + '/api/v1/messages')['messages']
            for message in messages:
                detail = self.json(self.endpoint('mailpit') + '/api/v1/message/' + message['ID'])
                if typ in detail.get('Text', '') or typ in detail.get('HTML', ''):
                    return {'message_id': message['ID'], 'subject': message['Subject']}
            return None
        delivery = eventually('Grafana DLT alert delivered to demo Mailpit', delivered, timeout=180)
        return {"type": typ, "alert": alert, "mailpit_delivery": delivery, "valid_business_case": case}

    def run(self):
        self.preflight()
        selected = SCENARIOS if self.args.scenario == "all" else (self.args.scenario,)
        self.report["checks"] = [{"name": name, "status": "not_run"} for name in selected]
        for check in self.report["checks"]:
            if self.report["cleanup_errors"]:
                break
            print(f"Chaos {check['name']}: running", flush=True)
            started = time.monotonic()
            try:
                check["evidence"] = getattr(self, check["name"])()
                check["status"] = "passed"
            except Exception as exc:
                check.update(status="failed", error=str(exc))
            finally:
                check["seconds"] = round(time.monotonic() - started, 2)
            print(f"Chaos {check['name']}: {check['status']} {check.get('error', '')}", flush=True)
        require(all(c["status"] == "passed" for c in self.report["checks"]) and not self.report["cleanup_errors"],
                "Chaos verification failed; see report (not_run checks are not passed)")


def arguments():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scenario", choices=("all", "saga", *SCENARIOS), default="all")
    parser.add_argument("--cluster", default=os.environ.get("BS_K8S_TEST_CLUSTER", "pg"))
    parser.add_argument("--preflight", action="store_true")
    parser.add_argument("--report", type=Path, default=Path("artifacts/k8s-chaos/report.json"))
    parser.add_argument("--stability-seconds", type=int, default=40)
    parser.add_argument("--outage-seconds", type=int, default=30)
    args = parser.parse_args()
    parser.error("stability must be >=35s and outage >=15s") if args.stability_seconds < 35 or args.outage_seconds < 15 else None
    return args


def main():
    args = arguments()
    stand = Stand(args)
    def interrupted(signum, frame):
        raise RuntimeError(f"Interrupted by signal {signum}; restoring active fault")
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        if args.preflight:
            stand.preflight()
        else:
            stand.run()
        return 0
    except Exception as exc:
        stand.report["error"] = str(exc)
        print(f"Chaos failed: {exc}", flush=True)
        return 1
    finally:
        stand.stack.close()
        if not args.preflight:
            args.report.parent.mkdir(parents=True, exist_ok=True)
            args.report.write_text(json.dumps(stand.report, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    raise SystemExit(main())
