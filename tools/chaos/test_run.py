import argparse
import base64
import copy
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("chaos_run", Path(__file__).with_name("run.py"))
chaos = importlib.util.module_from_spec(spec)
spec.loader.exec_module(chaos)


def stand():
    return chaos.Stand(argparse.Namespace(cluster="pg-drill", stability_seconds=40, outage_seconds=30, scenario="all"))


class EvidenceTests(unittest.TestCase):
    def test_missing_metrics_are_not_success(self):
        self.assertIsNone(chaos.vector_total({"status": "success", "data": {"resultType": "vector", "result": []}}))
        for value in ("NaN", "+Inf", "-1"):
            with self.subTest(value=value), self.assertRaises(RuntimeError):
                chaos.vector_total({"status": "success", "data": {"resultType": "vector", "result": [{"value": [1, value]}]}})

    def test_error_and_wrong_result_type_are_failures(self):
        for response in ({"status": "error"}, {"status": "success", "data": {"resultType": "scalar"}}):
            with self.subTest(response=response), self.assertRaises(RuntimeError):
                chaos.vector_total(response)

    def alert_response(self):
        return {"status": "success", "data": {"groups": [{"rules": [{
            "uid": chaos.RULE_UID, "health": "ok", "state": "firing",
            "alerts": [{"state": "Alerting", "activeAt": "2026-10-26T12:00:00Z",
                        "labels": {"topic": chaos.TOPIC, "type": "this-run"}}]}]}]}}

    def test_only_this_runs_real_firing_instance_counts(self):
        response = self.alert_response()
        since = 1793016000  # 2026-10-26T12:00:00Z
        self.assertIsNotNone(chaos.firing_instance(response, chaos.RULE_UID, "this-run", since))
        for mutation in ("pending", "wrong-rule", "wrong-type", "wrong-topic", "old"):
            value = copy.deepcopy(response)
            rule = value["data"]["groups"][0]["rules"][0]
            alert = rule["alerts"][0]
            if mutation == "pending":
                alert["state"] = "Pending"
            elif mutation == "wrong-rule":
                rule["uid"] = "replacement-rule"
            elif mutation == "wrong-type":
                alert["labels"]["type"] = "another-run"
            elif mutation == "wrong-topic":
                alert["labels"]["topic"] = "another-topic"
            else:
                alert["activeAt"] = "2026-10-25T12:00:00Z"
            with self.subTest(mutation=mutation):
                self.assertIsNone(chaos.firing_instance(value, chaos.RULE_UID, "this-run", since))

    def test_error_rule_cannot_establish_success(self):
        value = self.alert_response()
        value["data"]["groups"][0]["rules"][0]["health"] = "error"
        with self.assertRaises(RuntimeError):
            chaos.firing_instance(value, chaos.RULE_UID, "this-run", 0)

    def test_injector_cannot_access_api_or_broaden_network(self):
        job = chaos.injector_job("test", "marker", "typ")
        pod = job["spec"]["template"]
        self.assertEqual(pod["metadata"]["labels"]["app.kubernetes.io/name"], "kafka-topics")
        self.assertFalse(pod["spec"]["automountServiceAccountToken"])
        self.assertTrue(pod["spec"]["securityContext"]["runAsNonRoot"])
        security = pod["spec"]["containers"][0]["securityContext"]
        self.assertFalse(security["allowPrivilegeEscalation"])
        self.assertTrue(security["readOnlyRootFilesystem"])
        self.assertEqual(security["capabilities"], {"drop": ["ALL"]})


class FaultCleanupTests(unittest.TestCase):
    def controller(self, replicas=2):
        return {"metadata": {"uid": "original", "resourceVersion": "7"},
                "spec": {"replicas": replicas}, "status": {"readyReplicas": replicas}}

    def test_failure_restores_original_replica_count(self):
        runner = stand()
        calls = []
        with patch.object(runner, "obj", side_effect=[self.controller(), self.controller(0)]), \
                patch.object(runner, "pods", return_value=[]), \
                patch.object(runner, "kubectl", side_effect=lambda *a, **kw: calls.append(a)):
            with self.assertRaisesRegex(RuntimeError, "business assertion"):
                with runner.outage("bs-app", "deployment", "logistics-gateway"):
                    raise RuntimeError("business assertion")
        scales = [call for call in calls if "scale" in call]
        self.assertIn("--replicas=0", scales[0])
        self.assertIn("--resource-version=7", scales[0])
        self.assertIn("--replicas=2", scales[1])
        self.assertTrue(any("rollout" in call for call in calls))
        self.assertEqual(runner.report["cleanup_errors"], [])

    def test_restore_failure_stops_later_faults(self):
        runner = stand()
        wrong = self.controller(0)
        wrong["metadata"]["uid"] = "concurrent-replacement"
        with patch.object(runner, "obj", side_effect=[self.controller(), wrong]), \
                patch.object(runner, "pods", return_value=[]), patch.object(runner, "kubectl"):
            with self.assertRaisesRegex(RuntimeError, "replaced concurrently"):
                with runner.outage("bs-app", "deployment", "logistics-gateway"):
                    pass
        self.assertEqual(len(runner.report["cleanup_errors"]), 1)
        with patch.object(runner, "preflight"), patch.object(runner, "gateway") as gateway:
            with self.assertRaises(RuntimeError):
                runner.run()
        gateway.assert_not_called()
        self.assertTrue(all(c["status"] == "not_run" for c in runner.report["checks"]))

    def test_rejected_scale_does_not_overwrite_controller(self):
        runner = stand()
        with patch.object(runner, "obj", side_effect=[self.controller(), self.controller()]), \
                patch.object(runner, "kubectl", side_effect=RuntimeError("precondition rejected")) as kubectl:
            with self.assertRaisesRegex(RuntimeError, "precondition rejected"):
                with runner.outage("bs-app", "deployment", "logistics-gateway"):
                    self.fail("fault was not injected")
        self.assertEqual(kubectl.call_count, 1)
        self.assertEqual(runner.report["cleanup_errors"], [])

    def test_unreachable_api_during_cleanup_is_reported(self):
        runner = stand()
        with patch.object(runner, "obj", side_effect=[self.controller(), RuntimeError("API unavailable")]), \
                patch.object(runner, "pods", return_value=[]), patch.object(runner, "kubectl"):
            with self.assertRaisesRegex(RuntimeError, "API unavailable"):
                with runner.outage("bs-app", "deployment", "logistics-gateway"):
                    pass
        self.assertEqual(len(runner.report["cleanup_errors"]), 1)


class IsolationTests(unittest.TestCase):
    def test_opt_in_is_required_before_any_cluster_access(self):
        runner = stand()
        with patch.dict(chaos.os.environ, {}, clear=True), patch.object(runner, "kubectl") as kubectl:
            with self.assertRaisesRegex(RuntimeError, "BS_CHAOS_EPHEMERAL"):
                runner.preflight()
        kubectl.assert_not_called()

    def objects(self, namespace, kind, name):
        if kind == "deployment":
            return {"spec": {"template": {"spec": {"containers": [{"env": [{"name": "BS_DB_URL", "valueFrom": {
                "secretKeyRef": {"name": "parent-restored-transfer", "key": "BS_DB_URL"}}}]}]}}}}
        self.assertEqual(name, "parent-restored-transfer")
        return {"data": {"BS_DB_URL": base64.b64encode(self.db_url.encode()).decode()}}

    def test_sql_targets_restored_primary_and_own_role(self):
        runner = stand()
        self.db_url = "postgres://transfer:secret@pg-drill-rw.bs-data.svc:5432/transfer?sslmode=require"
        calls = []
        def kubectl(*args, **kwargs):
            calls.append(args)
            return '{"items":[{"metadata":{"name":"pg-drill-1"}}]}' if "get" in args else "1"
        with patch.object(runner, "obj", side_effect=self.objects), patch.object(runner, "kubectl", side_effect=kubectl):
            self.assertEqual(runner.sql("transfer", "SELECT count(*) FROM outbox"), "1")
        self.assertIn("cnpg.io/cluster=pg-drill,cnpg.io/instanceRole=primary", calls[0])
        self.assertIn("pg-drill-1", calls[1])
        self.assertIn("BEGIN READ ONLY; SET LOCAL ROLE transfer; SELECT count(*) FROM outbox; COMMIT;", calls[1])
        self.assertNotIn("secret", str(calls))

    def test_source_database_is_rejected_after_restore(self):
        runner = stand()
        self.db_url = "postgres://transfer:secret@pg-rw.bs-data.svc:5432/transfer"
        with patch.object(runner, "obj", side_effect=self.objects), patch.object(runner, "kubectl") as kubectl:
            with self.assertRaisesRegex(RuntimeError, "not been rerouted"):
                runner.sql("transfer", "SELECT 1")
        kubectl.assert_not_called()

    def test_parent_direct_env_reroute_is_supported(self):
        runner = stand()
        value = "postgres://transfer:secret@pg-drill-rw.bs-data.svc:5432/transfer?sslmode=require"
        deployment = {"spec": {"template": {"spec": {"containers": [{"env": [{"name": "BS_DB_URL", "value": value}]}]}}}}
        with patch.object(runner, "obj", return_value=deployment) as obj:
            self.assertEqual(runner.database("transfer").hostname, "pg-drill-rw.bs-data.svc")
        self.assertEqual(obj.call_count, 1)


if __name__ == "__main__":
    unittest.main()
