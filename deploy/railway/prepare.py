#!/usr/bin/env python3
"""Подготовить секреты и SQL для НОВОГО выделенного демо; ничего не отправляет и не выполняет."""

import argparse
import base64
import json
import pathlib
import re
import secrets
import urllib.parse

HERE = pathlib.Path(__file__).resolve().parent


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--public-url", required=True, help="HTTPS-домен edge без пути")
    parser.add_argument("--image-tag", required=True, help="один опубликованный sha-тег, например sha-0123456")
    args = parser.parse_args()
    address = urllib.parse.urlsplit(args.public_url)
    if address.scheme != "https" or not address.hostname or address.username or address.password or address.port not in (None, 443) or address.path not in ("", "/") or address.query or address.fragment:
        parser.error("нужен HTTPS-домен без пути, учётных данных, query и fragment")
    if not re.fullmatch(r"sha-[0-9a-f]{7,40}", args.image_tag):
        parser.error("для воспроизводимости укажите sha-тег")
    profile = json.loads((HERE / "profile.json").read_text(encoding="utf-8"))
    shared = {name: secrets.token_urlsafe(32) for name in profile["sharedVariables"]}
    shared.update({"PUBLIC_URL": args.public_url.rstrip("/"), "SESSION_KEY": base64.b64encode(secrets.token_bytes(32)).decode(), "K1_KEY": base64.b64encode(secrets.token_bytes(32)).decode(), "KAFKA_CLUSTER_ID": base64.urlsafe_b64encode(secrets.token_bytes(16)).decode().rstrip("=")})
    target = HERE / "prepared"
    # Не перезаписывать работающие ключи: повторная генерация нарушила бы cookie/K1/подключения к БД.
    target.mkdir(mode=0o700)
    sql = ["\\set ON_ERROR_STOP on", "-- Только новая выделенная установка; существующие роли и данные не удаляются."]
    for name, value in sorted(shared.items()):
        if not name.startswith("DB_"):
            continue
        role = name[3:-9].lower()
        sql += [f"CREATE ROLE {role} LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '{value}';", f"CREATE DATABASE {role} OWNER {role};", f"REVOKE ALL ON DATABASE {role} FROM PUBLIC;"]
    (target / "init.sql").write_text("\n".join(sql) + "\n", encoding="utf-8")
    (target / "shared.json").write_text(json.dumps(shared, indent=2) + "\n", encoding="utf-8")
    for name, service in profile["services"].items():
        service["image"] = service["image"].replace("<ONE_SHA_TAG>", args.image_tag)
        (target / (name + ".variables.json")).write_text(json.dumps(service["variables"], indent=2) + "\n", encoding="utf-8")
    (target / "profile.json").write_text(json.dumps(profile, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print("Подготовлены локальные файлы deploy/railway/prepared; секреты не выводятся. SQL и развёртывание не выполнялись.")


if __name__ == "__main__":
    main()
