#!/usr/bin/env python3
"""Схемы C4 для README: контекст, контейнеры, развёртывание. Запуск: python tools/docs-diagrams/c4.py."""

from svgkit import ROOT, Canvas

OUT = "docs/architecture/diagrams/c4"

LEGEND = [
    ("person", "Пользователь АС [Person]"),
    ("person-ext", "Внешний участник [Person]"),
    ("system", "АС [Software System]"),
    ("external", "Смежная система [Software System]"),
    ("both", "Запрос и ответ: 1-я строка — запрос, «Ответ:» — что возвращается"),
    ("line", "Передача в одну сторону"),
]


def context():
    c = Canvas("C4 · контекст", 2170, 1280)
    people = [
        ("phys", "Врач-трансфузиолог", "Вводит заявку с номером случая пациента, выбирает вариант"),
        ("op", "Оператор клинической организации", "Ведёт годовой расход, подтверждает передачу единиц"),
        ("an", "Территориальный аналитик", "Загружает статистику CSV, публикует версию оценки"),
        ("duty", "Дежурный смены", "Разбирает эскалации, назначает перевозку вручную"),
        ("it", "Внешняя ИТ-служба", "Ведёт правила подбора, читает журнал аудита"),
    ]
    for i, (ident, name, desc) in enumerate(people):
        c.person(ident, 40, 30 + 215 * i, 290, 200, name, "Person", desc)
    c.box("as", 760, 420, 340, 230, "АС распределения компонентов донорской крови", "Software System",
          "Запас против норматива, потребность районов, план по прогнозу ЧС, заявки врачей, реестр дефицита", kind="system", size=20)
    c.box("c01", 1400, 30, 320, 160, "C01 АИСТ-М", "Software System", "Учёт единиц от донации до выдачи", kind="external", size=20)
    c.box("c02", 1400, 300, 320, 160, "C02 Моделирование ЧС", "Software System", "Сценарии ЧС; передаёт в АС агрегированный прогноз", kind="external", size=20)
    c.box("c03", 1400, 570, 320, 160, "C03 Логистика", "Software System", "Служба перевозки субъекта обращения крови", kind="external", size=20)
    c.box("c04", 1400, 840, 320, 150, "C04 Почтовый сервис", "Software System", "Доставляет уведомления операторам и врачам", kind="external", size=20)
    c.person("spec", 1910, 250, 230, 220, "Специалист по моделированию ЧС", "Person", "Запускает моделирование", kind="person-ext", size=16)

    labels = [
        ("Заявка, выбор варианта", "Ответ: варианты, статус задания"),
        ("Годовой расход, подтверждение", "Ответ: запас, задания"),
        ("Файлы CSV, публикация оценки", "Ответ: проверка, версия оценки"),
        ("Эскалации, ручная перевозка", "Ответ: статусы обязательств"),
        ("Правила подбора, аудит", "Ответ: журнал, интеграции"),
    ]
    for i, ((ident, _, _), label) in enumerate(zip(people, labels)):
        c.edge((ident, "r", 0.5), ("as", "l", 0.12 + 0.19 * i), label=label, both=True)

    c.edge(("as", "t", 0.55), ("c01", "l", 0.3), via=[(947, 78)], both=True, at=(1175, 78),
           label=("Изменения, статус единиц, суточный снимок", "Ответ: изменения, статусы, файл CSV", "[ИНТ-01 · HTTPS/JSON, CSV]"))
    c.edge(("as", "t", 0.85), ("c01", "l", 0.75), via=[(1049, 150)], both=True, at=(1240, 150),
           label=("Закрепление единиц за пациентом", "Ответ: подтверждение приёма", "[ИНТ-05 · HTTPS/JSON]"))
    c.edge(("c02", "l", 0.5), ("as", "r", 0.2), via=[(1250, 380), (1250, 466)], both=True, at=(1250, 423),
           label=("Прогноз ЧС без ПДн", "Ответ: квитанция", "[ИНТ-02 · HTTPS/JSON]"))
    c.edge(("as", "r", 0.65), ("c03", "l", 0.5), via=[(1250, 569.5), (1250, 650)], both=True, at=(1250, 610),
           label=("Оценка времени, заказ перевозки", "Ответ: оценка, номер заказа", "[ИНТ-03 · HTTPS/JSON]"))
    c.edge(("as", "b", 0.5), ("c04", "l", 0.5), via=[(930, 915)], at=(930, 780),
           label=("Уведомление о задании", "[ИНТ-04 · SMTP/TLS]"))
    c.edge(("spec", "l", 0.435), ("c02", "r", 0.5), both=True, at=(1815, 380),
           label=("Сценарии, запуск", "Ответ: результаты"))
    c.legend(1400, 1030, LEGEND, width=560)
    return c.write(f"{OUT}/c4-context.drawio.svg")


SERVICES = [
    ("inventory", "inventory · ФП1", "Копия учёта АИСТ-М, запас, норматив, outbox ИНТ-05"),
    ("transfer", "transfer", "Обязательства O07, закрепления, сага подтверждения"),
    ("logistics", "logistics-gateway", "ACL к C03: заказ, сверка, аренда строки"),
    ("planning", "planning · ФП3", "Прогноз ЧС, целевой запас, план, реестр дефицита"),
    ("notification", "notification", "Письма, эскалации дежурному"),
    ("matching", "matching · ФП4", "Заявки с номером случая K1, правила, варианты"),
    ("demand", "demand · ФП2", "CSV аналитика, история расхода, версии оценки"),
    ("audit", "audit", "Неизменяемый журнал с цепочкой хешей"),
]


def containers():
    c = Canvas("C4 · контейнеры", 2230, 1500)
    c.person("users", 20, 380, 240, 230, "Пользователи АС", "Person", "Врач, оператор, аналитик, дежурный, ИТ-служба")

    c.boundary(340, 20, 320, 1190, "Сегмент доступа", "Сетевой сегмент · bs-edge")
    c.box("proxy", 370, 400, 260, 160, "Обратный прокси", "Container: Nginx / ingress-nginx", "TLS, единственный вход; маршруты к web, Keycloak и ИНТ-02")
    c.box("kc", 370, 700, 260, 160, "Keycloak", "Container: IdP, OIDC", "Учётные записи, роли, clinic_id в токене, 2FA для K1")

    c.boundary(700, 20, 770, 1190, "Основная сеть", "Сетевой сегмент · bs-app")
    c.box("web", 720, 400, 230, 160, "Веб-интерфейс web", "Container: Go, html/template, HTMX", "Экраны пяти ролей, сессия в cookie, живые статусы SSE")
    c.queue("kafka", 720, 900, 230, 130, "Kafka", "Container: KRaft, 1 брокер", "outbox → топики → inbox, ключ — id агрегата")
    c.boundary(1060, 35, 380, 1110, "Сервисы контекстов", "Container: Go · своя база у каждого", ident="svc")
    y = 50
    for ident, name, desc in SERVICES:
        c.box(ident, 1090, y, 320, 105, name, "Container: Go", desc, size=16)
        y += 165 if ident == "inventory" else 128

    c.boundary(1520, 720, 360, 490, "Сегмент данных", "Сетевой сегмент · bs-data")
    c.database("pg", 1580, 830, 240, 200, "PostgreSQL 16", "Container: CloudNativePG", "База и роль на сервис, реплика, WAL-архив, PITR")

    c.box("c01", 1920, 47, 280, 110, "C01 АИСТ-М", "Software System", kind="external", size=18)
    c.box("c03", 1920, 340, 280, 110, "C03 Логистика", "Software System", kind="external", size=18)
    c.box("c02", 1920, 468, 280, 110, "C02 Моделирование ЧС", "Software System", kind="external", size=18)
    c.box("c04", 1920, 596, 280, 110, "C04 Почтовый сервис", "Software System", kind="external", size=18)

    c.edge(("users", "r", 0.35), ("proxy", "l", 0.4), both=True, at=(315, 465), label=("Запросы", "Ответ: страницы", "[HTTPS]"))
    c.edge(("users", "r", 0.75), ("kc", "l", 0.5), both=True, label=("Вход, второй фактор", "Ответ: сессия", "[OIDC через прокси]"))
    c.edge(("proxy", "r", 0.5), ("web", "l", 0.5), both=True, at=(680, 480), label=("Запросы", "[HTTP]"))
    c.edge(("web", "b", 0.35), ("kc", "r", 0.5), via=[(800.5, 780)], both=True, at=(740, 680),
           label=("Обмен кода на токены", "Ответ: JWT", "[OIDC code + PKCE]"))
    c.edge(("web", "r", 0.5), ("svc", "l", 0.411), both=True, at=(1005, 480), label=("Команды", "[HTTP/JSON, JWT]"))
    c.edge(("transfer", "t", 0.5), ("inventory", "b", 0.5), at=(1250, 182), label=("Статус единиц, 3 с", "[HTTP, внутренний]"))
    c.edge(("svc", "l", 0.8289), ("kafka", "r", 0.5), both=True, at=(1005, 965), label=("События", "[outbox → inbox]"))
    c.edge(("svc", "r", 0.8063), ("pg", "l", 0.5), at=(1510, 930), label=("SQL", "[своя база]"))
    c.edge(("inventory", "r", 0.5), ("c01", "l", 0.5), both=True, at=(1680, 102),
           label=("Изменения, статус, закрепление", "Ответ: изменения, статусы, CSV", "[ИНТ-01, ИНТ-05 · HTTPS/JSON]"))
    c.edge(("logistics", "r", 0.5), ("c03", "l", 0.5), both=True, at=(1680, 395),
           label=("Оценка времени, заказ", "Ответ: оценка, номер заказа", "[ИНТ-03 · HTTPS/JSON]"))
    c.edge(("c02", "l", 0.5), ("planning", "r", 0.5), both=True, at=(1680, 523), label=("Прогноз ЧС", "Ответ: квитанция", "[ИНТ-02 · через прокси]"))
    c.edge(("notification", "r", 0.5), ("c04", "l", 0.5), at=(1680, 651), label=("Письмо оператору", "[ИНТ-04 · SMTP/TLS]"))

    c.legend(30, 1250, [
        ("person", "Пользователь АС [Person]"),
        ("container", "Контейнер АС [Container]"),
        ("external", "Смежная система [Software System]"),
        ("both", "Запрос и ответ: 1-я строка — запрос, «Ответ:» — что возвращается"),
        ("line", "Передача в одну сторону"),
    ], width=560)
    return c.write(f"{OUT}/c4-containers.drawio.svg")


def deployment():
    c = Canvas("C4 · развёртывание", 2230, 1650)
    c.person("user", 30, 120, 250, 210, "Пользователь", "Person", "Браузер на рабочем месте клиники")
    c.node(320, 20, 1890, 1360, "Кластер kind", "Deployment Node: Kubernetes 1.33, CNI Calico, NetworkPolicy", fill="#FFFFFF", ident="cluster")

    c.node(350, 90, 380, 250, "ingress-nginx", "Namespace", fill="#F4F6F8")
    c.box("ing", 380, 160, 320, 150, "ingress-nginx", "Container: NGINX, hostPort 80/443", "TLS; / → web, /auth → Keycloak, ИНТ-02 → planning")
    c.node(760, 90, 380, 250, "bs-edge", "Namespace · сегмент доступа", fill="#F4F6F8")
    c.box("kc", 790, 160, 320, 150, "Keycloak", "Container: Deployment", "Realm blood-supply, OIDC, роли и clinic_id")
    c.node(1170, 90, 380, 250, "cnpg-system", "Namespace", fill="#F4F6F8")
    c.box("op", 1200, 160, 320, 150, "Оператор CloudNativePG", "Container: Deployment", "Ведёт кластер PostgreSQL в bs-data: реплика, архив WAL, PITR")
    c.node(1580, 90, 600, 250, "bs-observability", "Namespace · зона контроля, только приём", fill="#F4F6F8")
    c.box("otel", 1610, 160, 540, 150, "otel-lgtm", "Container: grafana/otel-lgtm", "OTel Collector, Grafana, Prometheus, Tempo, Loki; маскирование номера случая")

    c.node(350, 370, 1830, 540, "bs-app", "Namespace · основная сеть", fill="#F4F6F8", ident="app")
    c.box("web", 380, 470, 280, 160, "web", "Container: Deployment · HPA", "SSR + HTMX, сессии, SSE")
    c.boundary(710, 445, 1100, 345, "8 сервисов контекстов", "Deployment · PDB · probes", ident="svc")
    names = ["inventory", "demand", "planning", "matching", "transfer", "logistics-gateway", "notification", "audit"]
    for i, name in enumerate(names):
        c.box(f"s{i}", 735 + 265 * (i % 4), 460 + 140 * (i // 4), 245, 120, name, "Container: Deployment", "HPA" if name in ("inventory", "matching") else "", size=16)
    c.queue("kafka", 1880, 473, 280, 150, "Kafka", "Container: StatefulSet", "KRaft, 1 брокер; топики создаёт Job")
    c.note(880, 815, 740, "У каждого сервиса: probes /healthz и /readyz, ресурсы, запуск без root, read-only FS, PodDisruptionBudget. "
                           "Ретранслятор outbox один на сервис через advisory lock. Default deny: разрешены только описанные потоки.")

    c.node(350, 950, 1080, 400, "bs-data", "Namespace · сегмент данных", fill="#F4F6F8")
    c.database("pg1", 380, 1030, 260, 180, "PostgreSQL 16.9", "Container: CNPG Cluster · primary", "База и роль на сервис")
    c.database("pg2", 760, 1030, 260, 180, "PostgreSQL 16.9", "Container: CNPG Cluster · replica", "Потоковая реплика")
    c.box("minio", 1130, 1030, 270, 180, "MinIO", "Container: Deployment", "Архив WAL и копии, хранение 7 дней, PITR")
    c.node(1460, 950, 720, 400, "bs-sims", "Namespace · только оверлеи local и ci", fill="#F4F6F8", ident="sims")
    for i, (name, desc) in enumerate([("sim-aist", "Имитатор C01 АИСТ-М"), ("sim-emergency", "Имитатор C02 Моделирование ЧС"),
                                      ("sim-logistics", "Имитатор C03 Логистика"), ("Mailpit", "Имитатор C04: SMTP и просмотр писем")]):
        c.box(f"sim{i}", 1490 + 345 * (i % 2), 1030 + 175 * (i // 2), 315, 145, name, "Container: Deployment", desc, kind="external", size=16)

    c.edge(("user", "r", 0.3407), ("ing", "l", 0.5), both=True, at=(315, 235), label=("HTTPS · 443",))
    c.edge(("ing", "r", 0.5), ("kc", "l", 0.5), at=(745, 235), label=("/auth",))
    c.edge(("ing", "b", 0.85), ("web", "t", 0.5), via=[(652, 440), (520, 440)], at=(652, 395), label=("/", "[HTTP]"))
    c.edge(("web", "r", 0.5), ("svc", "l", 0.3043), at=(685, 550), label=("HTTP", "[JWT]"))
    c.edge(("svc", "r", 0.3), ("kafka", "l", 0.5), both=True, at=(1845, 548), label=("События",))
    c.edge(("svc", "t", 0.9), ("otel", "b", 0.3), via=[(1700, 360), (1772, 360)], dashed=True, at=(1735, 360), label=("OTLP", "[трассы, метрики, логи]"))
    c.edge(("svc", "b", 0.12), ("pg1", "t", 0.95), via=[(842, 930), (627, 930)], at=(735, 930), label=("SQL · 5432", "[только из bs-app]"))
    c.edge(("pg1", "r", 0.5), ("pg2", "l", 0.5), at=(700, 1120), label=("Репликация",))
    c.edge(("pg1", "b", 0.5), ("minio", "b", 0.5), via=[(510, 1260), (1265, 1260)], at=(887, 1260), label=("Архив WAL, копии", "[S3, barmanObjectStore]"))
    c.edge(("svc", "b", 0.85), ("sims", "t", 0.4), via=[(1645, 930), (1748, 930)], at=(1700, 930), label=("ИНТ-01…05, SMTP", "[HTTP к имитаторам]"))

    c.legend(320, 1410, [
        ("person", "Пользователь [Person]"),
        ("node", "Узел развёртывания: кластер, namespace [Deployment Node]"),
        ("container", "Экземпляр контейнера АС [Container]"),
        ("external", "Имитатор смежной системы, только стенд разработки"),
        ("line", "Поток данных"),
        ("dashed", "Телеметрия, только приём"),
    ], width=620)
    return c.write(f"{OUT}/c4-deployment.drawio.svg")


if __name__ == "__main__":
    for build in (context, containers, deployment):
        print(build().relative_to(ROOT))
