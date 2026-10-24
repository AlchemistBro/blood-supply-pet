#!/usr/bin/env python3
"""Статическая ER-модель из Up-миграций; SQL и подключения к БД не выполняются."""

import argparse
import hashlib
import pathlib
import re
import sys


ROOT = pathlib.Path(__file__).resolve().parents[2]
OUTPUT = ROOT / "docs/architecture/er"
IDENT = r"[a-z_][a-z0-9_]*"
TOKEN = re.compile(
    r"'(?:''|[^'])*'|\$(?P<tag>[a-zA-Z_][a-zA-Z_0-9]*|)\$.*?\$(?P=tag)\$"
    r'|"(?:""|[^"])*"|--[^\n]*|/\*.*?\*/|.', re.S
)


def split_sql(text, separator):
    """Разделители внутри литералов, функций и скобок не являются границами."""
    parts, current, depth = [], [], 0
    for match in TOKEN.finditer(text):
        token = match.group()
        if token.startswith(("--", "/*")):
            current.append(" ")
            continue
        if token == "(":
            depth += 1
        elif token == ")":
            depth -= 1
        if token == separator and depth == 0:
            parts.append("".join(current).strip())
            current = []
        else:
            current.append(token)
    if depth != 0:
        raise ValueError("несбалансированные скобки SQL")
    if "".join(current).strip():
        parts.append("".join(current).strip())
    return parts


def names(text):
    result = [part.strip() for part in text.split(",")]
    if not all(re.fullmatch(IDENT, name) for name in result):
        raise ValueError("неподдерживаемое выражение вместо имени столбца: " + text)
    return result


def constraint(table, definition):
    definition = re.sub(r"^CONSTRAINT\s+" + IDENT + r"\s+", "", definition, flags=re.I)
    primary = re.match(r"PRIMARY KEY\s*\(([^)]+)\)", definition, re.I)
    unique = re.match(r"UNIQUE(?:\s+NULLS\s+NOT\s+DISTINCT)?\s*\(([^)]+)\)", definition, re.I)
    foreign = re.match(r"FOREIGN KEY\s*\(([^)]+)\)\s+REFERENCES\s+(" + IDENT + r")\s*\(([^)]+)\)", definition, re.I)
    if primary:
        table["primary"] = names(primary[1])
    elif unique:
        table["unique"].append(names(unique[1]))
    elif foreign:
        table["foreign"].append((names(foreign[1]), foreign[2], names(foreign[3])))
    elif not re.match(r"CHECK\s*\(", definition, re.I):
        raise ValueError("неподдерживаемое ограничение: " + definition)


def column(table, definition):
    found = re.match(r"(" + IDENT + r")\s+([a-z]+(?:\s+precision|\s+varying)?(?:\s*\([^)]*\))?(?:\[\])?)(.*)$", definition, re.I | re.S)
    if not found:
        raise ValueError("неподдерживаемый столбец: " + definition)
    name, kind, tail = found.groups()
    if name in table["columns"]:
        raise ValueError("повторный столбец: " + name)
    table["columns"][name] = {"type": re.sub(r"\s+", "", kind), "required": bool(re.search(r"\bNOT NULL\b|\bPRIMARY KEY\b", tail, re.I))}
    if re.search(r"\bPRIMARY KEY\b", tail, re.I):
        table["primary"].append(name)
    if re.search(r"\bUNIQUE\b", tail, re.I):
        table["unique"].append([name])
    foreign = re.search(r"\bREFERENCES\s+(" + IDENT + r")\s*\(([^)]+)\)", tail, re.I)
    if foreign:
        table["foreign"].append(([name], foreign[1], names(foreign[2])))


def apply(tables, sql):
    for statement in split_sql(sql, ";"):
        create = re.fullmatch(r"CREATE TABLE\s+(" + IDENT + r")\s*\((.*)\)", statement, re.I | re.S)
        alter = re.fullmatch(r"ALTER TABLE\s+(" + IDENT + r")\s+(.*)", statement, re.I | re.S)
        if create:
            name, body = create.groups()
            if name in tables:
                raise ValueError("повторная таблица: " + name)
            table = {"columns": {}, "primary": [], "unique": [], "foreign": []}
            for definition in split_sql(body, ","):
                if re.match(r"(?:CONSTRAINT|PRIMARY|FOREIGN|UNIQUE|CHECK)\b", definition, re.I):
                    constraint(table, definition)
                else:
                    column(table, definition)
            tables[name] = table
        elif alter:
            table = tables[alter[1]]
            for action in split_sql(alter[2], ","):
                if re.match(r"ADD COLUMN\s+", action, re.I):
                    column(table, re.sub(r"^ADD COLUMN\s+", "", action, flags=re.I))
                elif re.match(r"ADD CONSTRAINT\s+", action, re.I):
                    constraint(table, re.sub(r"^ADD\s+", "", action, flags=re.I))
                elif re.fullmatch(r"DROP CONSTRAINT\s+" + IDENT, action, re.I):
                    # Текущие Up-миграции меняют здесь только CHECK; PK/FK не удаляются.
                    if not action.lower().endswith("_check"):
                        raise ValueError("удаление ключевого ограничения требует поддержки: " + action)
                else:
                    raise ValueError("неподдерживаемый ALTER: " + action)
        elif re.match(r"(?:CREATE|ALTER|DROP)\s+TABLE\b", statement, re.I):
            raise ValueError("неподдерживаемый DDL таблицы: " + statement)


def render(service, tables, sources):
    digest = hashlib.sha256()
    for path, up in sources:
        digest.update(path.encode())
        digest.update(up.encode())
    lines = [f"# ER: {service}", "", "Сгенерировано `task docs:er` из Up-миграций. Не редактировать вручную.", "", f"SHA-256 исходников: `{digest.hexdigest()}`.", "", "Показаны столбцы, первичные и внешние ключи. CHECK, индексы, триггеры и данные не отображаются.", "Справочники и платформенные таблицы включены так же, как при запуске сервиса.", "Типы с параметрами показаны без параметров; массивы сохранены.", "", "```mermaid", "erDiagram"]
    for name, table in sorted(tables.items()):
        lines.append(f"    {name} {{")
        foreign_columns = {col for cols, _, _ in table["foreign"] for col in cols}
        for col, value in table["columns"].items():
            keys = [key for key, yes in [("PK", col in table["primary"]), ("FK", col in foreign_columns)] if yes]
            kind = re.sub(r"\([^)]*\)", "", value["type"])
            lines.append(f"        {kind} {col}" + (" " + ", ".join(keys) if keys else ""))
        lines.append("    }")
        for cols, parent, targets in table["foreign"]:
            if parent not in tables or not all(c in tables[parent]["columns"] for c in targets):
                raise ValueError(f"неизвестная ссылка {name} → {parent}.{targets}")
            required = all(table["columns"][c]["required"] or c in table["primary"] for c in cols)
            unique = any(set(key).issubset(cols) for key in [table["primary"]] + table["unique"] if key)
            left, right = ("||" if required else "|o"), ("o|" if unique else "o{")
            lines.append(f'    {parent} {left}--{right} {name} : "{", ".join(cols)}"')
    lines += ["```", "", "Исходники:", ""]
    lines += [f"- [{path}](../../../{path})" for path, _ in sources]
    return "\n".join(lines) + "\n"


def generate():
    result = {}
    for folder in sorted((ROOT / "services").iterdir()):
        if not (folder / "migrations").is_dir():
            continue
        paths = []
        for source in [ROOT / "libs/messaging/migrations", ROOT / "libs/refdata/migrations", folder / "migrations"]:
            paths.extend(sorted(source.glob("*.sql")))
        sources, tables = [], {}
        for path in paths:
            text = path.read_text(encoding="utf-8").replace("\r\n", "\n")
            if "-- +goose Up" not in text or "-- +goose Down" not in text:
                raise ValueError(f"нет явных границ Up/Down: {path}")
            up = text.split("-- +goose Up", 1)[1].split("-- +goose Down", 1)[0]
            try:
                apply(tables, up)
            except (ValueError, KeyError) as err:
                raise ValueError(f"{path.relative_to(ROOT)}: {err}") from err
            sources.append((path.relative_to(ROOT).as_posix(), up))
        result[f"{folder.name}.md"] = render(folder.name, tables, sources)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="сравнить с сохранёнными схемами без записи")
    args = parser.parse_args()
    generated = generate()
    stale = []
    for name, content in generated.items():
        path = OUTPUT / name
        if args.check:
            if not path.exists() or path.read_text(encoding="utf-8") != content:
                stale.append(name)
        else:
            OUTPUT.mkdir(parents=True, exist_ok=True)
            path.write_text(content, encoding="utf-8", newline="\n")
    orphaned = {path.name for path in OUTPUT.glob("*.md")} - generated.keys()
    if stale or orphaned:
        print("ER-схемы расходятся с миграциями:", ", ".join(sorted(set(stale) | orphaned)), file=sys.stderr)
        return 1
    print(f"ER-схемы: {len(generated)} баз, {'сверены' if args.check else 'обновлены'}; SQL не выполнялся")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, KeyError) as error:
        print(error, file=sys.stderr)
        sys.exit(1)
