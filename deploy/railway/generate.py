#!/usr/bin/env python3
"""Создать или сверить несекретные шаблоны Railway без обращения к платформе."""

import argparse
import json
import pathlib
import sys

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[1]
APPS = ["audit", "inventory", "demand", "planning", "matching", "transfer", "logistics-gateway", "notification", "web"]
SIMS = ["sim-aist", "sim-emergency", "sim-logistics"]


def private(name, path="", port=8080):
    return "http://${{" + name + ".RAILWAY_PRIVATE_DOMAIN}}:" + str(port) + path


def shared(name):
    return "${{shared." + name + "}}"


def documents():
    config = {"$schema": "https://railway.com/railway.schema.json", "deploy": {"numReplicas": 1, "healthcheckPath": "/readyz", "healthcheckTimeout": 180, "restartPolicyType": "ON_FAILURE", "restartPolicyMaxRetries": 10}}
    common = {"PORT": "8080", "BS_HTTP_ADDR": "[::]:8080", "BS_ENV": "demo", "BS_LOG_LEVEL": "info", "BS_AUTH_DISABLED": "false"}
    auth = {"BS_OIDC_ISSUER": shared("PUBLIC_URL") + "/oidc/realms/blood-supply", "BS_OIDC_JWKS_URL": private("edge", "/oidc/realms/blood-supply/protocol/openid-connect/certs"), "BS_OIDC_AUDIENCE": "blood-supply-api", "BS_KAFKA_BROKERS": "${{kafka.RAILWAY_PRIVATE_DOMAIN}}:29092"}
    token = {"BS_SERVICE_TOKEN_URL": private("edge", "/oidc/realms/blood-supply/protocol/openid-connect/token"), "BS_SERVICE_CLIENT_ID": "services", "BS_SERVICE_CLIENT_SECRET": shared("SERVICE_CLIENT_SECRET")}
    services = {}
    for name in APPS + SIMS:
        variables = dict(common)
        if name in APPS:
            variables.update(auth)
            if name != "web":
                db = "logistics" if name == "logistics-gateway" else name
                variables["BS_DB_URL"] = "postgres://" + db + ":" + shared("DB_" + db.upper() + "_PASSWORD") + "@${{postgres.RAILWAY_PRIVATE_DOMAIN}}:5432/" + db + "?sslmode=disable"
        services[name] = {"image": "ghcr.io/alchemistbro/blood-supply-" + name + ":<ONE_SHA_TAG>", "public": False, "variables": variables, "deployConfig": "app.railway.json"}
    services["inventory"]["variables"].update({"BS_AIST_URL": private("sim-aist"), "BS_K1_KEYS": "demo1:" + shared("K1_KEY")})
    services["matching"]["variables"]["BS_K1_KEYS"] = "demo1:" + shared("K1_KEY")
    services["transfer"]["variables"].update(token | {"BS_INVENTORY_URL": private("inventory")})
    services["logistics-gateway"]["variables"]["BS_C03_URL"] = private("sim-logistics")
    services["notification"]["variables"].update({"BS_SMTP_ADDR": "${{mailpit.RAILWAY_PRIVATE_DOMAIN}}:1025", "BS_WEB_BASE_URL": shared("PUBLIC_URL")})
    services["sim-aist"]["variables"].update({"BS_SIM_SEED_ON_START": "true", "BS_SIM_SEED_UNITS": "10000"})
    services["sim-emergency"]["variables"].update(token | {"BS_PLANNING_URL": private("planning")})
    web = services["web"]["variables"]
    for name in APPS:
        if name == "web":
            continue
        key = "LOGISTICS" if name == "logistics-gateway" else name.upper()
        web["BS_" + key + "_URL"] = private(name)
    web.update({"BS_WEB_PUBLIC_URL": shared("PUBLIC_URL"), "BS_OIDC_DISCOVERY_URL": private("edge", "/oidc/realms/blood-supply"), "BS_OIDC_CLIENT_SECRET": shared("WEB_CLIENT_SECRET"), "BS_SESSION_KEY": shared("SESSION_KEY"), "BS_COOKIE_SECURE": "true", "BS_HSTS_MAX_AGE": "31536000", "BS_SERVICE_CLIENT_SECRET": shared("SERVICE_CLIENT_SECRET")})
    services["edge"] = {"image": "ghcr.io/alchemistbro/blood-supply-railway-edge:<ONE_SHA_TAG>", "public": True, "variables": {"PORT": "8080", "WEB_UPSTREAM": "${{web.RAILWAY_PRIVATE_DOMAIN}}:8080", "KEYCLOAK_UPSTREAM": "${{keycloak.RAILWAY_PRIVATE_DOMAIN}}:8080"}, "deployConfig": "edge.railway.json"}
    services["keycloak"] = {"image": "ghcr.io/alchemistbro/blood-supply-railway-keycloak:<ONE_SHA_TAG>", "public": False, "variables": {"PORT": "8080", "KC_DB_URL": "jdbc:postgresql://${{postgres.RAILWAY_PRIVATE_DOMAIN}}:5432/keycloak", "KC_DB_USERNAME": "keycloak", "KC_DB_PASSWORD": shared("DB_KEYCLOAK_PASSWORD"), "KC_HOSTNAME": shared("PUBLIC_URL") + "/oidc", "KC_HOSTNAME_BACKCHANNEL_DYNAMIC": "true", "KC_HTTP_ENABLED": "true", "KC_HTTP_RELATIVE_PATH": "/oidc", "KC_PROXY_HEADERS": "xforwarded", "KC_BOOTSTRAP_ADMIN_USERNAME": "owner", "KC_BOOTSTRAP_ADMIN_PASSWORD": shared("KEYCLOAK_ADMIN_PASSWORD"), "BS_WEB_PUBLIC_URL": shared("PUBLIC_URL"), "BS_WEB_CLIENT_SECRET": shared("WEB_CLIENT_SECRET"), "BS_SERVICE_CLIENT_SECRET": shared("SERVICE_CLIENT_SECRET")}, "healthcheckPath": "/oidc/realms/blood-supply/.well-known/openid-configuration", "healthcheckTimeout": 300}
    services["postgres"] = {"image": "ghcr.io/alchemistbro/blood-supply-railway-postgres:<ONE_SHA_TAG>", "public": False, "variables": {"POSTGRES_PASSWORD": shared("POSTGRES_ADMIN_PASSWORD"), "PGDATA": "/var/lib/postgresql/data/pgdata"}, "volume": "/var/lib/postgresql/data"}
    services["kafka"] = {"image": "ghcr.io/alchemistbro/blood-supply-railway-kafka:<ONE_SHA_TAG>", "public": False, "volume": "/var/lib/kafka/data", "variables": {"CLUSTER_ID": shared("KAFKA_CLUSTER_ID"), "KAFKA_NODE_ID": "1", "KAFKA_PROCESS_ROLES": "broker,controller", "KAFKA_LISTENERS": "INTERNAL://[::]:29092,CONTROLLER://[::]:9093", "KAFKA_ADVERTISED_LISTENERS": "INTERNAL://${{kafka.RAILWAY_PRIVATE_DOMAIN}}:29092", "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP": "INTERNAL:PLAINTEXT,CONTROLLER:PLAINTEXT", "KAFKA_INTER_BROKER_LISTENER_NAME": "INTERNAL", "KAFKA_CONTROLLER_LISTENER_NAMES": "CONTROLLER", "KAFKA_CONTROLLER_QUORUM_VOTERS": "1@${{kafka.RAILWAY_PRIVATE_DOMAIN}}:9093", "KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR": "1", "KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR": "1", "KAFKA_TRANSACTION_STATE_LOG_MIN_ISR": "1", "KAFKA_AUTO_CREATE_TOPICS_ENABLE": "false", "KAFKA_LOG_DIRS": "/var/lib/kafka/data"}}
    services["mailpit"] = {"image": "ghcr.io/alchemistbro/blood-supply-railway-mailpit:<ONE_SHA_TAG>", "public": False, "variables": {"MP_SMTP_BIND_ADDR": "[::]:1025", "MP_UI_BIND_ADDR": "[::]:8025", "MP_MAX_MESSAGES": "500"}}
    services["seed"] = {"image": "ghcr.io/alchemistbro/blood-supply-railway-seed:<ONE_SHA_TAG>", "public": False, "runOnce": True, "variables": {"BS_SEED_" + name.upper(): private(name) for name in ["inventory", "demand", "planning", "matching"]} | {"BS_SEED_KEYCLOAK": private("edge", "/oidc"), "BS_SEED_AIST": private("sim-aist"), "BS_SEED_SERVICE_SECRET": shared("SERVICE_CLIENT_SECRET")}, "restartPolicyType": "NEVER"}
    profile = {"description": "Перечень настроек для оператора, не импортируемый Railway template. Настройки deploy переносить в dashboard для image services.", "sharedVariables": ["PUBLIC_URL", "WEB_CLIENT_SECRET", "SERVICE_CLIENT_SECRET", "SESSION_KEY", "K1_KEY", "KEYCLOAK_ADMIN_PASSWORD", "POSTGRES_ADMIN_PASSWORD", "KAFKA_CLUSTER_ID"] + ["DB_" + name.upper() + "_PASSWORD" for name in ["audit", "inventory", "demand", "planning", "matching", "transfer", "logistics", "notification", "keycloak"]], "services": services}
    realm = json.loads((ROOT / "deploy/compose/keycloak/realm-blood-supply.json").read_text(encoding="utf-8"))
    realm["sslRequired"] = "external"
    for client in realm["clients"]:
        if client["clientId"] == "web":
            client["secret"] = "${BS_WEB_CLIENT_SECRET}"
            client["redirectUris"] = ["${BS_WEB_PUBLIC_URL}/auth/callback"]
            client["webOrigins"] = ["${BS_WEB_PUBLIC_URL}"]
            client["attributes"]["post.logout.redirect.uris"] = "${BS_WEB_PUBLIC_URL}/login"
        elif client["clientId"] == "services":
            client["secret"] = "${BS_SERVICE_CLIENT_SECRET}"
    edge_config = {"$schema": config["$schema"], "deploy": dict(config["deploy"]) | {"healthcheckPath": "/healthz"}}
    return {"app.railway.json": config, "edge.railway.json": edge_config, "profile.json": profile, "keycloak/realm-blood-supply.json": realm}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    stale = []
    for name, document in documents().items():
        path = HERE / name
        content = json.dumps(document, ensure_ascii=False, indent=2) + "\n"
        if args.check:
            if not path.exists() or path.read_text(encoding="utf-8") != content:
                stale.append(name)
        else:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content, encoding="utf-8", newline="\n")
    if stale:
        print("Устарели шаблоны Railway: " + ", ".join(stale), file=sys.stderr)
        return 1
    print("Шаблоны Railway сверены" if args.check else "Шаблоны Railway обновлены")
    return 0


if __name__ == "__main__":
    sys.exit(main())
