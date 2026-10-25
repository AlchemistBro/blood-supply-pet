#!/usr/bin/env python3
"""UML-диаграмма последовательности в оформлении StarUML: от заявки врача до заказа перевозки.

Запуск: python tools/docs-diagrams/sequence.py. Шаги сверены с matching (Create, Select), transfer (Confirm,
allocatePatient) и logistics-gateway; описание саги — docs/architecture/saga-confirmation.md.
"""

from svgkit import FONT, ROOT, esc, measure

SIZE = 13
ROW = 30
HEAD = 40
TOP = 60

LIFELINES = [
    ("D", "Врач", "actor"),
    ("O", "Оператор", "actor"),
    ("W", "web", "object"),
    ("M", "matching", "object"),
    ("T", "transfer", "object"),
    ("K", "Kafka", "object"),
    ("I", "inventory", "object"),
    ("L", "logistics-gateway", "object"),
    ("N", "notification", "object"),
    ("A", "C01 АИСТ-М", "object"),
    ("C", "C03 Логистика", "object"),
    ("P", "C04 Почта", "object"),
]

# ("call"|"async"|"reply", от, кому, текст) · ("self", кто, текст) · ("on"|"off", кто)
# ("frag", вид, левая, правая, условие) · ("else", условие) · ("end",)
STEPS = [
    ("on", "D"),
    ("call", "D", "W", "создать заявку(номер случая, вид, группа, срочность)"), ("on", "W"),
    ("call", "W", "M", "POST /requests"), ("on", "M"),
    ("frag", "critical", "M", "M", "[одна транзакция БД]"),
    ("self", "M", "зашифровать номер случая (K1)"),
    ("self", "M", "подобрать варианты по версиям правил"),
    ("self", "M", "outbox: ClinicalRequestCreated, VariantsOffered"),
    ("end",),
    ("reply", "M", "W", "варианты с рангом и объяснением"), ("off", "M"),
    ("reply", "W", "D", "варианты: местный и из C001"), ("off", "W"),
    ("call", "D", "W", "выбрать вариант(C001)"), ("on", "W"),
    ("call", "W", "M", "POST /requests/{id}/select"), ("on", "M"),
    ("self", "M", "переоценить единицы по свежим проекциям"),
    ("async", "M", "K", "OpenObligation → transfer.commands"),
    ("reply", "M", "W", "TASK_OPENED"), ("off", "M"),
    ("reply", "W", "D", "задание отправлено в C001"), ("off", "W"), ("off", "D"),
    ("async", "K", "T", "OpenObligation"), ("on", "T"),
    ("self", "T", "O07 PENDING, срок по срочности"),
    ("async", "T", "K", "ObligationOpened"), ("off", "T"),
    ("async", "K", "N", "ObligationOpened"), ("on", "N"),
    ("async", "N", "P", "письмо оператору C001 (ИНТ-04)"), ("off", "N"),
    ("on", "O"),
    ("call", "O", "W", "подтвердить единицы(FEFO)"), ("on", "W"),
    ("call", "W", "T", "POST /obligations/{id}/confirm"), ("on", "T"),
    ("call", "T", "I", "GET /internal/units/status (3 с)"), ("on", "I"),
    ("call", "I", "A", "статус единиц"), ("on", "A"),
    ("reply", "A", "I", "статусы"), ("off", "A"),
    ("reply", "I", "T", "на месте / выбыли"), ("off", "I"),
    ("frag", "alt", "W", "I", "[все единицы выбыли]"),
    ("reply", "T", "W", "Rejected"),
    ("else", "[АИСТ-М не ответила за 3 с]"),
    ("reply", "T", "W", "SourceSystemUnavailable"),
    ("else", "[единицы на месте]"),
    ("frag", "critical", "T", "I", "[одна транзакция БД]"),
    ("self", "T", "заблокировать Остаток источника"),
    ("self", "T", "CAS PENDING → CONFIRMED, закрепления"),
    ("self", "T", "outbox: TransferConfirmed, RequestTransport, AllocateUnitsToPatient"),
    ("end",),
    ("reply", "T", "W", "Confirmed"),
    ("end",), ("off", "T"),
    ("reply", "W", "O", "«Закреплено, готовьте»"), ("off", "W"), ("off", "O"),
    ("frag", "par", "T", "C", "[заказ перевозки]"),
    ("async", "K", "L", "RequestTransport"), ("on", "L"),
    ("call", "L", "C", "найти заказ(clientRef = o07-…)"), ("on", "C"),
    ("reply", "C", "L", "не найден"), ("off", "C"),
    ("call", "L", "C", "создать заказ"), ("on", "C"),
    ("reply", "C", "L", "shipmentRef"), ("off", "C"),
    ("async", "L", "K", "TransportAccepted"), ("off", "L"),
    ("async", "K", "T", "TransportAccepted"), ("on", "T"),
    ("self", "T", "CAS CONFIRMED → TRANSPORT_REQUESTED"),
    ("async", "T", "K", "TransportRequested"), ("off", "T"),
    ("else", "[закрепление за пациентом]"),
    ("async", "K", "I", "AllocateUnitsToPatient"), ("on", "I"),
    ("call", "I", "A", "закрепить единицы (ИНТ-05)"), ("on", "A"),
    ("reply", "A", "I", "принято"), ("off", "A"),
    ("async", "I", "K", "PatientAllocationAcknowledged"), ("off", "I"),
    ("end",),
    ("async", "K", "W", "TransportRequested"), ("on", "W"),
    ("async", "W", "D", "SSE: перевозка заказана"),
    ("async", "W", "O", "SSE: перевозка заказана"), ("off", "W"),
]


def layout():
    """Расставляет линии жизни так, чтобы подпись каждого сообщения помещалась между концами стрелки."""
    names = {ident: label for ident, label, _ in LIFELINES}
    order = [ident for ident, _, _ in LIFELINES]
    gaps = [max(150, (measure(names[a], SIZE, True) + measure(names[b], SIZE, True)) / 2 + 40) for a, b in zip(order, order[1:])]
    number = 0
    needs = []
    for step in STEPS:
        if step[0] in ("call", "async", "reply"):
            number += 1
            i, j = sorted((order.index(step[1]), order.index(step[2])))
            needs.append((i, j, measure(f"{number}: {step[3]}", SIZE) + 40))
        elif step[0] == "self":
            number += 1
            i = order.index(step[1])
            needs.append((i, i + 1, measure(f"{number}: {step[2]}", SIZE) + 60))
    for i, j, need in sorted(needs, key=lambda n: n[1] - n[0]):
        have = sum(gaps[i:j])
        if have < need:
            for k in range(i, j):
                gaps[k] += (need - have) / (j - i)
    xs, x = {}, 90
    for ident, gap in zip(order, gaps + [0]):
        xs[ident] = x
        x += gap
    return xs, x + 60


def render():
    xs, width = layout()
    order = [ident for ident, _, _ in LIFELINES]
    lines, acts, frags, msgs, texts = [], [], [], [], []
    active = {ident: [] for ident in order}
    stack = []
    y = TOP + HEAD + 40
    number = 0

    def edge_x(ident, toward):
        depth = len(active[ident])
        offset = 0 if depth == 0 else 7 + 6 * (depth - 1)
        return xs[ident] + (offset if toward > xs[ident] else -offset)

    def label(x, y_text, content, anchor="middle"):
        w = measure(content, SIZE)
        left = x - w / 2 if anchor == "middle" else x
        texts.append(f'<rect x="{left - 3:.1f}" y="{y_text - SIZE:.1f}" width="{w + 6:.1f}" height="{SIZE + 4}" fill="#FFFFFF"/>'
                     f'<text x="{x:.1f}" y="{y_text:.1f}" font-size="{SIZE}" text-anchor="{anchor}">{esc(content)}</text>')

    def arrowhead(x, yy, direction, filled):
        tip, back, half = x, x - 10 * direction, 5
        if filled:
            return f'<path d="M{tip:.1f},{yy} L{back:.1f},{yy - half} L{back:.1f},{yy + half} z" fill="#000000"/>'
        return f'<path d="M{back:.1f},{yy - half} L{tip:.1f},{yy} L{back:.1f},{yy + half}" fill="none" stroke="#000000" stroke-width="1"/>'

    skip = set()
    for index, step in enumerate(STEPS):
        kind = step[0]
        if index in skip:
            continue
        if kind in ("call", "async") and STEPS[index + 1] == ("on", step[2]):
            # Активация получателя начинается до стрелки: стрелка упирается в край полосы, как в StarUML.
            active[step[2]].append(y - 8)
            skip.add(index + 1)
        if kind == "on":
            active[step[1]].append(y - 8)
        elif kind == "off":
            start = active[step[1]].pop()
            acts.append((step[1], len(active[step[1]]), start, y - 12))
        elif kind in ("call", "async", "reply"):
            number += 1
            src, dst, content = step[1:]
            x1 = edge_x(src, xs[dst])
            x2 = edge_x(dst, xs[src])
            direction = 1 if x2 > x1 else -1
            dash = ' stroke-dasharray="6 4"' if kind == "reply" else ""
            msgs.append(f'<path d="M{x1:.1f},{y} H{x2:.1f}" stroke="#000000" stroke-width="1"{dash}/>' + arrowhead(x2, y, direction, kind == "call"))
            label((x1 + x2) / 2, y - 6, f"{number}: {content}")
            y += ROW
        elif kind == "self":
            number += 1
            who, content = step[1:]
            x1 = edge_x(who, xs[who] + 1)
            x2 = xs[who] + 40
            msgs.append(f'<path d="M{x1:.1f},{y - 4} H{x2:.1f} V{y + 12} H{x1 + 1:.1f}" fill="none" stroke="#000000" stroke-width="1"/>' + arrowhead(x1 + 1, y + 12, -1, True))
            label(x2 + 8, y + 7, f"{number}: {content}", anchor="start")
            y += ROW + 8
        elif kind == "frag":
            _, operator, left, right, guard = step
            depth = len(stack)
            margin = 46 - 12 * depth
            frame = {"op": operator, "x1": xs[left] - margin, "x2": xs[right] + margin + (220 if operator == "critical" and left == right else 0), "y1": y - 14, "seps": [], "guard": guard}
            stack.append(frame)
            y += 26
        elif kind == "else":
            stack[-1]["seps"].append((y - 14, step[1]))
            y += 24
        elif kind == "end":
            frame = stack.pop()
            frame["y2"] = y - 10
            frags.append(frame)
            y += 10

    height = y + 40
    for ident, depth, y1, y2 in acts:
        x = xs[ident] - 7 + 6 * depth
        lines.append(f'<rect x="{x:.1f}" y="{y1:.1f}" width="14" height="{y2 - y1:.1f}" fill="#FFFFFF" stroke="#000000" stroke-width="1"/>')

    frag_svg = []
    for f in sorted(frags, key=lambda f: f["x2"] - f["x1"], reverse=True):
        tag = measure(f["op"], SIZE, True) + 18
        frag_svg.append(f'<rect x="{f["x1"]:.1f}" y="{f["y1"]:.1f}" width="{f["x2"] - f["x1"]:.1f}" height="{f["y2"] - f["y1"]:.1f}" fill="none" stroke="#000000" stroke-width="1"/>'
                        f'<path d="M{f["x1"]:.1f},{f["y1"] + 20:.1f} H{f["x1"] + tag - 6:.1f} L{f["x1"] + tag:.1f},{f["y1"] + 14:.1f} V{f["y1"]:.1f}" fill="#FFFFFF" stroke="#000000" stroke-width="1"/>'
                        f'<text x="{f["x1"] + 7:.1f}" y="{f["y1"] + 15:.1f}" font-size="{SIZE}" font-weight="bold">{esc(f["op"])}</text>'
                        f'<text x="{f["x1"] + tag + 10:.1f}" y="{f["y1"] + 15:.1f}" font-size="{SIZE}">{esc(f["guard"])}</text>')
        for sep, guard in f["seps"]:
            frag_svg.append(f'<path d="M{f["x1"]:.1f},{sep:.1f} H{f["x2"]:.1f}" stroke="#000000" stroke-width="1" stroke-dasharray="6 4"/>'
                            f'<text x="{f["x1"] + 10:.1f}" y="{sep + 16:.1f}" font-size="{SIZE}">{esc(guard)}</text>')

    heads = []
    for ident, name, kind in LIFELINES:
        x = xs[ident]
        lines.insert(0, f'<path d="M{x},{TOP + HEAD} V{height - 30}" stroke="#000000" stroke-width="1" stroke-dasharray="6 4"/>')
        if kind == "actor":
            top = TOP - 18
            heads.append(f'<g fill="#FFFFFF" stroke="#000000" stroke-width="1.2"><circle cx="{x}" cy="{top + 7}" r="7"/>'
                         f'<path d="M{x},{top + 14} V{top + 32} M{x - 11},{top + 20} H{x + 11} M{x},{top + 32} L{x - 9},{top + 44} M{x},{top + 32} L{x + 9},{top + 44}" fill="none"/></g>'
                         f'<text x="{x}" y="{TOP + HEAD + 12}" font-size="{SIZE}" text-anchor="middle">{esc(name)}</text>')
        else:
            w = measure(name, SIZE) + 24
            heads.append(f'<rect x="{x - w / 2:.1f}" y="{TOP}" width="{w:.1f}" height="{HEAD}" fill="#FFFFFF" stroke="#000000" stroke-width="1"/>'
                         f'<text x="{x}" y="{TOP + HEAD / 2 + 5}" font-size="{SIZE}" text-anchor="middle">{esc(name)}</text>')

    title = "sd Заявка врача: подбор, подтверждение, перевозка"
    tag = measure(title, SIZE, True) + 22
    frame = (f'<rect x="10" y="10" width="{width - 20:.1f}" height="{height - 20:.1f}" fill="none" stroke="#000000" stroke-width="1"/>'
             f'<path d="M10,32 H{10 + tag - 8:.1f} L{10 + tag:.1f},24 V10" fill="#FFFFFF" stroke="#000000" stroke-width="1"/>'
             f'<text x="18" y="26" font-size="{SIZE}" font-weight="bold">{esc(title)}</text>')
    body = "".join([frame] + lines + frag_svg + msgs + texts + heads)
    return (f'<svg xmlns="http://www.w3.org/2000/svg" width="{width:.0f}" height="{height:.0f}" viewBox="0 0 {width:.0f} {height:.0f}" font-family="{FONT}">'
            f'<title>{esc(title)}</title><rect width="100%" height="100%" fill="#FFFFFF"/>{body}</svg>\n')


if __name__ == "__main__":
    path = ROOT / "docs/architecture/diagrams/uml/sequence-request-to-transport.svg"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(render(), encoding="utf-8", newline="\n")
    print(path.relative_to(ROOT))
