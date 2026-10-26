#!/usr/bin/env python3
"""Статическая проверка ссылок и DI BPMN, без браузера и исполнения процессов."""

import pathlib
import sys
import xml.etree.ElementTree as ET

ROOT = pathlib.Path(__file__).resolve().parents[2]
MODEL = "http://www.omg.org/spec/BPMN/20100524/MODEL"
DI = "http://www.omg.org/spec/BPMN/20100524/DI"


def main():
    paths = sorted((ROOT / "docs/architecture/diagrams/bpmn").rglob("*.bpmn"))
    documents = [(path, ET.parse(path).getroot()) for path in paths]
    processes = {element.get("id") for _, root in documents for element in root.findall(f"{{{MODEL}}}process")}
    failures = []
    nodes = {"task", "userTask", "serviceTask", "sendTask", "receiveTask", "manualTask", "businessRuleTask", "scriptTask", "subProcess", "callActivity", "startEvent", "endEvent", "boundaryEvent", "intermediateCatchEvent", "intermediateThrowEvent", "exclusiveGateway", "inclusiveGateway", "parallelGateway", "eventBasedGateway"}
    for path, root in documents:
        ids = {}

        def fail(message):
            failures.append(f"{path.relative_to(ROOT)}: {message}")

        for element in root.iter():
            identity = element.get("id")
            if identity:
                if identity in ids:
                    fail("повторный id " + identity)
                ids[identity] = element
        drawn = {element.get("bpmnElement") for element in root.iter() if element.tag in {f"{{{DI}}}BPMNShape", f"{{{DI}}}BPMNEdge"}}
        for element in root.iter():
            for attribute in ["sourceRef", "targetRef", "attachedToRef", "bpmnElement", "processRef", "messageRef", "errorRef", "escalationRef", "default"]:
                value = element.get(attribute)
                if value and value not in ids:
                    fail(f"неизвестный {attribute}={value}")
            called = element.get("calledElement")
            if called and called not in processes:
                fail("неизвестный вызываемый процесс " + called)
            if element.tag in {f"{{{MODEL}}}incoming", f"{{{MODEL}}}outgoing", f"{{{MODEL}}}flowNodeRef"} and element.text not in ids:
                fail("неизвестная ссылка " + str(element.text))
            local = element.tag.removeprefix("{" + MODEL + "}")
            if local in nodes | {"sequenceFlow", "association"} and element.get("id") not in drawn:
                fail("нет DI для " + str(element.get("id")))
    if not paths:
        failures.append("BPMN не найдены")
    if failures:
        print("\n".join(failures), file=sys.stderr)
        return 1
    print(f"BPMN: {len(paths)} файлов, XML, ссылки и DI сверены; процессы не исполнялись")
    return 0


if __name__ == "__main__":
    sys.exit(main())
