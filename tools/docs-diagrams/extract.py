#!/usr/bin/env python3
"""Извлечь исходники и перечень архитектурных SVG, не запуская браузер."""

import json
import pathlib
import re

ROOT = pathlib.Path(__file__).resolve().parents[2]
OUTPUT = ROOT / "dist/diagrams"


def main():
    OUTPUT.mkdir(parents=True, exist_ok=True)
    items = []
    for path in sorted((ROOT / "docs").rglob("*.md")) + [ROOT / "README.md", ROOT / "README.en.md"]:
        for number, block in enumerate(re.findall(r"```mermaid\s*\n(.*?)```", path.read_text(encoding="utf-8"), re.S), start=1):
            source = path.relative_to(ROOT).as_posix()
            stem = source.removesuffix(".md").replace("/", "__") + f"--{number}"
            (OUTPUT / (stem + ".mmd")).write_text(block, encoding="utf-8", newline="\n")
            items.append({"source": source, "input": stem + ".mmd", "svg": stem + ".svg", "kind": "mermaid"})
    for path in sorted((ROOT / "docs/architecture/diagrams/bpmn").glob("*.bpmn")):
        items.append({"source": path.relative_to(ROOT).as_posix(), "svg": "bpmn--" + path.stem + ".svg", "kind": "bpmn"})
    if not items:
        raise ValueError("исходники диаграмм не найдены")
    (OUTPUT / "manifest.json").write_text(json.dumps(items, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    index = ["# Архитектурные SVG", "", "Производные от исходников репозитория. Сгенерированы задачей docs:diagrams.", ""]
    index += [f'- [{item["source"]}]({item["svg"]})' for item in items]
    (OUTPUT / "index.md").write_text("\n".join(index) + "\n", encoding="utf-8")
    print(f"Подготовлено {len(items)} диаграмм; рендер не запускался")


if __name__ == "__main__":
    main()
