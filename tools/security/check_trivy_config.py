"""Проверка полного отчёта Trivy с исключениями для конкретных ресурсов."""

import argparse
import json
from pathlib import Path
import re
import sys


OVERLAYS = {"local", "ci", "prod-example"}
TOKEN_URL = "http://keycloak.bs-edge.svc:8080/auth/realms/blood-supply/protocol/openid-connect/token"
WRITABLE = {
    "bs-app_apps_v1_statefulset_kafka.yaml": (
        "StatefulSet", "bs-app", "kafka", "apache/kafka-native:3.9.1",
        "скрипт образа создаёт /opt/kafka/config/server.properties",
    ),
    "bs-edge_apps_v1_deployment_keycloak.yaml": (
        "Deployment", "bs-edge", "keycloak", "quay.io/keycloak/keycloak:26.4",
        "start без --optimized пересобирает /opt/keycloak/lib",
    ),
    "bs-observability_apps_v1_deployment_otel-lgtm.yaml": (
        "Deployment", "bs-observability", "otel-lgtm", "grafana/otel-lgtm:0.11.0",
        "демонстрационный образ инициализирует собственные каталоги",
    ),
}


def exception_reason(target, finding, root):
    parts = target.split("/")
    if len(parts) != 2 or parts[0] not in OVERLAYS:
        return None
    if finding.get("Severity") != "HIGH" or finding.get("Status") != "FAIL":
        return None
    name = parts[1]
    resource = WRITABLE.get(name)
    url_map = re.fullmatch(
        r"(bs-app_v1_configmap_platform-env|bs-sims_v1_configmap_sims-env)-([a-z0-9]{10})\.yaml", name
    )
    if finding.get("ID") == "KSV-0014" and resource:
        kind, namespace, container, image, reason = resource
        expected = (
            f"Container '{container}' of {kind} '{container}' should set "
            "'securityContext.readOnlyRootFilesystem' to true"
        )
        if finding.get("Message") != expected:
            return None
    elif finding.get("ID") == "KSV-0109" and url_map:
        namespace, container = (
            ("bs-app", "platform-env") if name.startswith("bs-app_") else ("bs-sims", "sims-env")
        )
        kind = "ConfigMap"
        container += "-" + url_map[2]
        expected = (
            f"ConfigMap '{container}' in '{namespace}' namespace stores secrets in key(s) or value(s) "
            "'{\"BS_SERVICE_TOKEN_URL\"}'"
        )
        if finding.get("Message") != expected:
            return None
        reason = "BS_SERVICE_TOKEN_URL содержит публичный адрес endpoint, а не токен"
    else:
        return None

    path = (root / target).resolve()
    if not path.is_relative_to(root.resolve()):
        return None
    source = path.read_text(encoding="utf-8")
    if not all(line in source.splitlines() for line in (
        "kind: " + kind, "  name: " + container, "  namespace: " + namespace
    )):
        return None
    if resource:
        images = re.findall(r"^\s+(?:- )?image: (\S+)\s*$", source, re.MULTILINE)
        if images != [image]:
            return None
    else:
        urls = re.findall(r"^  BS_SERVICE_TOKEN_URL: (.*)$", source, re.MULTILINE)
        if urls != [TOKEN_URL]:
            return None
    return reason


def evaluate(report, root):
    if report.get("SchemaVersion") != 2 or not isinstance(report.get("Results"), list) or not report["Results"]:
        raise ValueError("неполный или неподдерживаемый отчёт Trivy")
    accepted, blocked = [], []
    for result in report["Results"]:
        target = result["Target"]
        if not isinstance(target, str) or not result.get("Type"):
            raise ValueError("в отчёте отсутствуют путь или тип проверенного файла")
        for category in ("Misconfigurations", "Vulnerabilities", "Secrets"):
            findings = result.get(category) or []
            if not isinstance(findings, list):
                raise ValueError("неподдерживаемый формат списка находок")
            for finding in findings:
                severity = finding.get("Severity")
                if severity not in {"UNKNOWN", "LOW", "MEDIUM", "HIGH", "CRITICAL"}:
                    raise ValueError("неизвестная важность находки")
                if severity not in {"HIGH", "CRITICAL"}:
                    continue
                reason = None
                if category == "Misconfigurations" and result.get("Type") == "kubernetes":
                    reason = exception_reason(target, finding, root)
                identifier = finding.get("ID") or finding.get("VulnerabilityID") or finding.get("RuleID")
                if reason:
                    accepted.append((target, identifier, reason))
                else:
                    blocked.append((target, identifier))
    return accepted, blocked


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("report", type=Path)
    parser.add_argument("config_root", type=Path)
    args = parser.parse_args()
    try:
        report = json.loads(args.report.read_text(encoding="utf-8"))
        accepted, blocked = evaluate(report, args.config_root)
    except (OSError, ValueError, KeyError, TypeError, AttributeError) as error:
        print(f"Ошибка проверки Trivy: {error}", file=sys.stderr)
        return 2
    for target, identifier, reason in accepted:
        print(f"Исключение: {target}: {identifier}: {reason}")
    for target, identifier in blocked:
        print(f"Блокирует CI: {target}: {identifier}")
    print(f"Trivy: согласованных исключений {len(accepted)}, блокирующих находок {len(blocked)}")
    return 1 if blocked else 0


if __name__ == "__main__":
    sys.exit(main())
