# Карта ограниченных контекстов (целевая)

Источник: схема 10 «Карта контекстов (целевая)» из плана миграции, уточнена под Go-реализацию.

```mermaid
flowchart LR
    subgraph ext[Внешние системы]
        C01[C01 АИСТ-М]
        C02[C02 Моделирование ЧС<br/>внешний домен D02]
        C03[C03 Логистика]
        C04[C04 Почта]
    end

    subgraph d01[D01 Обеспечение клиник компонентами донорской крови]
        INV[Запасы · inventory<br/>Supporting · ФП1<br/>O01 O02 O03, водяной знак]
        DEM[Территориальная потребность · demand<br/>Supporting · ФП2<br/>O04 O05 O16 O17]
        PLN[Планирование пополнения · planning<br/>Core · ФП3<br/>O06, план, прогноз ЧС, O11]
        MAT[Подбор по заявке · matching<br/>Core · ФП4<br/>O08 O09 O10 O15]
        TRF[Передачи и обязательства · transfer<br/>Core · новый контекст<br/>O07 O18, Остаток источника, сага]
        LOG[Логистика-шлюз · logistics-gateway<br/>Generic · ACL]
        NTF[Уведомления · notification<br/>Generic]
        AUD[Аудит · audit<br/>Generic · неизменяемый журнал]
        IAM[Идентификация и доступ<br/>Generic · Keycloak]
    end

    C01 -->|"U→D · Conformist + ACL<br/>изменения, снимок, статус"| INV
    INV -->|"OHS/PL: снимок запаса + водяной знак"| MAT
    INV -->|"OHS/PL: снимок запаса + водяной знак"| TRF
    INV -->|"OHS/PL: снимок, норматив"| PLN
    DEM -->|"U→D · Customer–Supplier<br/>PL: версия оценки, доли"| PLN
    MAT <-->|"Customer–Supplier<br/>выбор + спецификация замены ↓<br/>статусы, недопоставка ↑"| TRF
    PLN <-->|"Customer–Supplier<br/>предложения ↓ · статусы, недопоставка ↑"| TRF
    C02 -->|"U→D · PL + ACL<br/>белый список, без ПДн"| PLN
    MAT -->|"оценки времени (проекция)"| LOG
    TRF -->|"RequestTransport / CancelTransport"| LOG
    LOG -->|"D · Conformist"| C03
    TRF -->|"PL: события статусов"| NTF
    NTF -->|"D · Conformist"| C04
    TRF -->|"ИНТ-05: AllocateToPatient (команда)"| INV
    INV -->|"закрепление за пациентом, outbox"| C01
    PLN -.->|AuditEvent| AUD
    MAT -.->|AuditEvent| AUD
    TRF -.->|AuditEvent| AUD
    INV -.->|AuditEvent| AUD
    DEM -.->|AuditEvent| AUD
```

Обозначения: U — upstream, D — downstream; OHS/PL — открытый сервис и опубликованный язык; ACL — антикоррупционный слой.

## Отношения и что они значат для кода

| Отношение | Тип | Правило |
| --- | --- | --- |
| C01 → inventory | Conformist + ACL | Формат АИСТ-М не выходит за пределы `inventory/internal/adapters/aist`. Внутрь публикуются только события `UnitRegistered`, `UnitUpdated`, `UnitWithdrawn`, `StockSnapshotPublished` |
| inventory → transfer, matching, planning | OHS / PL | Снимок запаса и единицы с версией (водяной знак). Потребители держат проекцию и показывают «данные на …». Деградация при задержке, а не зависание |
| transfer → inventory | единственный синхронный вызов | `GET /internal/units/status?ids=` перед транзакцией подтверждения (ADR-0004) |
| demand → planning | Customer–Supplier, PL | `DemandEstimatePublished` с версией и строками `clinic_demand`; planning строит план только по опубликованной версии |
| matching ↔ transfer | Customer–Supplier | matching шлёт команду `OpenObligation` (выбор врача, спецификация замены, fallback-источники); transfer отвечает событиями статусов и `TransferShortfallRecorded`; matching реагирует подбором следующего варианта или дефицитом |
| planning ↔ transfer | Customer–Supplier | planning шлёт `OpenObligation` по предложениям плана; читает статусы и недопоставки, пересчитывает покрытие |
| C02 → planning | PL + ACL | Входной фильтр: белый список полей, отбрасывание всего остального; версия сценария уникальна |
| transfer → logistics-gateway → C03 | ACL | Команды и ответы через Kafka; шлюз держит сверку, аренду, кэш оценок времени |
| transfer → notification → C04 | PL | notification подписан на `transfer.events`; сбой письма не отменяет задание (FR044) |
| все → audit | PL | Каждый сервис пишет `AuditRecorded` в свой outbox; audit строит цепочку хешей |
| Keycloak | Generic | Роль и `clinic_id` в токене; сервисы проверяют JWT по JWKS |

## Что принадлежит кому (владение данными)

| Данные | Владелец | Кто держит копию |
| --- | --- | --- |
| Единицы, статусы, норматив, годовой расход, SyncRun | inventory | transfer (`unit_copy`, `source_stock`), matching (`unit_projection`), planning (`stock_projection`) |
| Файлы CSV, профили, история расхода, доли, версии оценки | demand | planning (`demand_projection` по опубликованной версии) |
| Прогноз ЧС, целевой запас, план, предложения, реестр дефицита | planning | — (matching шлёт `RequestDeficitRecorded`, planning проецирует в O11) |
| Заявки, ограничения, правила O15, варианты, выбор | matching | — |
| Обязательства, закрепления, Остаток источника | transfer | matching/planning (`obligation_status_projection`) |
| Заказы перевозки, оценки времени | logistics-gateway | matching, planning (`delivery_estimate_projection`) |
| Справочники (клиники, районы, виды, группы, срочность) | `libs/refdata` (статический набор, одинаковый для всех), в проде — событие `ReferenceDataChanged` | каждый сервис в своей БД |
| Учётные записи и роли | Keycloak | — |
| Журнал аудита | audit | — |
