import copy
from pathlib import Path
import tempfile
import unittest

from check_trivy_config import OVERLAYS, TOKEN_URL, WRITABLE, evaluate


class PolicyTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def resource(self, filename, kind, namespace, name, extra):
        target = "ci/" + filename
        path = self.root / target
        path.parent.mkdir(exist_ok=True)
        path.write_text(
            f"kind: {kind}\nmetadata:\n  name: {name}\n  namespace: {namespace}\n{extra}\n",
            encoding="utf-8",
        )
        return target

    def report(self, target, identifier, message):
        return {"SchemaVersion": 2, "Results": [{
            "Target": target, "Type": "kubernetes", "Misconfigurations": [{
                "ID": identifier, "Message": message, "Severity": "HIGH", "Status": "FAIL",
            }],
        }]}

    def writable(self, filename=None):
        filename = filename or next(iter(WRITABLE))
        kind, namespace, name, image, _ = WRITABLE[filename]
        image_line = "- image" if name == "otel-lgtm" else "image"
        target = self.resource(filename, kind, namespace, name, f"spec:\n  {image_line}: {image}")
        return self.report(target, "KSV-0014", (
            f"Container '{name}' of {kind} '{name}' should set "
            "'securityContext.readOnlyRootFilesystem' to true"
        ))

    def url(self):
        name = "platform-env-0123456789"
        target = self.resource(
            f"bs-app_v1_configmap_{name}.yaml", "ConfigMap", "bs-app", name,
            f"data:\n  BS_SERVICE_TOKEN_URL: {TOKEN_URL}",
        )
        return self.report(target, "KSV-0109", (
            f"ConfigMap '{name}' in 'bs-app' namespace stores secrets in key(s) or value(s) "
            "'{\"BS_SERVICE_TOKEN_URL\"}'"
        ))

    def assert_blocked(self, report):
        accepted, blocked = evaluate(report, self.root)
        self.assertEqual(accepted, [])
        self.assertEqual(len(blocked), 1)

    def test_only_three_reviewed_images_are_accepted_in_each_overlay(self):
        for filename in WRITABLE:
            report = self.writable(filename)
            for overlay in OVERLAYS:
                item = copy.deepcopy(report)
                target = item["Results"][0]["Target"]
                source = (self.root / target).read_bytes()
                item["Results"][0]["Target"] = target.replace("ci/", overlay + "/")
                path = self.root / item["Results"][0]["Target"]
                path.parent.mkdir(exist_ok=True)
                path.write_bytes(source)
                accepted, blocked = evaluate(item, self.root)
                self.assertEqual(len(accepted), 1)
                self.assertEqual(blocked, [])

    def test_image_or_namespace_changes_require_review(self):
        for old, new in (("apache/kafka-native:3.9.1", "other/image:latest"), ("bs-app", "other")):
            report = self.writable()
            path = self.root / report["Results"][0]["Target"]
            path.write_text(path.read_text().replace(old, new))
            self.assert_blocked(report)

    def test_different_container_resource_rule_or_severity_is_not_accepted(self):
        for key, value in (("Message", "Container 'sidecar' has a writable root"),
                           ("ID", "KSV-0118"), ("Severity", "CRITICAL")):
            report = self.writable()
            report["Results"][0]["Misconfigurations"][0][key] = value
            self.assert_blocked(report)
        report = self.writable()
        report["Results"][0]["Target"] = "ci/bs-app_apps_v1_deployment_web.yaml"
        self.assert_blocked(report)

    def test_only_public_endpoint_is_accepted(self):
        report = self.url()
        accepted, blocked = evaluate(report, self.root)
        self.assertEqual(len(accepted), 1)
        self.assertEqual(blocked, [])
        path = self.root / report["Results"][0]["Target"]
        path.write_text(path.read_text().replace(TOKEN_URL, TOKEN_URL + "?token=synthetic"))
        self.assert_blocked(report)

    def test_password_in_same_configmap_is_not_accepted(self):
        report = self.url()
        finding = report["Results"][0]["Misconfigurations"][0]
        finding["Message"] = finding["Message"].replace(
            '{"BS_SERVICE_TOKEN_URL"}', '{"BS_SERVICE_TOKEN_URL", "PASSWORD"}'
        )
        self.assert_blocked(report)

    def test_other_configmap_and_unreviewed_path_are_not_accepted(self):
        for target in ("ci/bs-app_v1_configmap_other-0123456789.yaml",
                       "other/bs-app_v1_configmap_platform-env-0123456789.yaml",
                       "../bs-app_v1_configmap_platform-env-0123456789.yaml"):
            report = self.url()
            report["Results"][0]["Target"] = target
            self.assert_blocked(report)

    def test_cves_and_secret_findings_never_use_config_exceptions(self):
        for category in ("Vulnerabilities", "Secrets"):
            report = self.writable()
            result = report["Results"][0]
            result[category] = result.pop("Misconfigurations")
            self.assert_blocked(report)

    def test_incomplete_reports_and_unknown_severity_fail_closed(self):
        for report in ({}, {"SchemaVersion": 2, "Results": []},
                       {"SchemaVersion": 2, "Results": [{"Target": "file.yaml"}]}):
            with self.assertRaises(ValueError):
                evaluate(report, self.root)
        report = self.writable()
        report["Results"][0]["Misconfigurations"][0].pop("Severity")
        with self.assertRaises(ValueError):
            evaluate(report, self.root)


if __name__ == "__main__":
    unittest.main()
