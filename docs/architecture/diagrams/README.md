# Архитектурные схемы

Все схемы лежат в репозитории исходниками: Mermaid внутри Markdown и BPMN 2.0 XML. Источник истины — исходник;
картинки (SVG) только производные от него.

## Перечень

| Вид | Схема | Файл | Формат |
| --- | --- | --- | --- |
| C4, уровень 1 | Контекст системы, смежные системы и имитаторы | [c4-context.md](../c4-context.md) | Mermaid `C4Context` |
| C4, уровень 2 | Контейнеры: сервисы, базы, Kafka, платформа | [c4-containers.md](../c4-containers.md) | Mermaid `C4Container` |
| C4, уровень 3 | Компоненты `transfer`, `logistics-gateway`, `planning`, `matching` | [c4-components.md](../c4-components.md) | Mermaid `C4Component` / `flowchart` |
| Карта контекстов | Ограниченные контексты DDD и отношения | [context-map.md](../../domain/context-map.md) | Mermaid `flowchart` |
| UML, состояния | Обязательство O07 | [uml.md](../uml.md#состояния-обязательства-o07), [obligation-states.md](../../domain/obligation-states.md) | Mermaid `stateDiagram-v2` |
| UML, состояния | Заказ перевозки `transport_request` | [uml.md](../uml.md#состояния-заказа-перевозки-transport_request) | Mermaid `stateDiagram-v2` |
| UML, последовательность | Сага подтверждения и компенсации | [saga-confirmation.md](../saga-confirmation.md) | Mermaid `sequenceDiagram` |
| UML, последовательность | Сверка шлюза при потерянном ответе C03 | [uml.md](../uml.md#сверка-шлюза-при-потерянном-ответе-c03) | Mermaid `sequenceDiagram` |
| ER | Модель данных по базам сервисов | [data-model.md](../data-model.md) | Mermaid `erDiagram` |
| ER физических таблиц | Восемь баз, включая платформенные таблицы и новые столбцы | [описание генерации](../../../tools/docs-er/README.md) | Mermaid `erDiagram`, генерация из Up-миграций |
| Развёртывание | Сетевые сегменты и namespace | [deployment.md](../deployment.md) | Mermaid `flowchart` |
| BPMN | Подтверждение передачи | [bpmn/confirm-transfer.bpmn](bpmn/confirm-transfer.bpmn) | BPMN 2.0 XML + DI |
| BPMN | Планирование пополнения по прогнозу ЧС | [bpmn/replenishment-plan.bpmn](bpmn/replenishment-plan.bpmn) | BPMN 2.0 XML + DI |
| BPMN | Обработка заявки врача | [bpmn/clinical-request.bpmn](bpmn/clinical-request.bpmn) | BPMN 2.0 XML + DI |
| BPMN | Загрузка CSV и публикация оценки | [bpmn/csv-demand.bpmn](bpmn/csv-demand.bpmn) | BPMN 2.0 XML + DI |

Подробнее о BPMN-процессах и соглашениях моделирования — [bpmn/README.md](bpmn/README.md).

## C4 и UML для README

| Вид | Схема | Файл |
| --- | --- | --- |
| C4, уровень 1 | Контекст: роли, АС, C01–C04 | [c4/c4-context.drawio.svg](c4/c4-context.drawio.svg) |
| C4, уровень 2 | Контейнеры по сетевым сегментам | [c4/c4-containers.drawio.svg](c4/c4-containers.drawio.svg) |
| C4, развёртывание | Кластер kind, namespace, CloudNativePG, имитаторы | [c4/c4-deployment.drawio.svg](c4/c4-deployment.drawio.svg) |
| UML, последовательность | От заявки врача до заказа перевозки и закрепления в АИСТ-М | [uml/sequence-request-to-transport.svg](uml/sequence-request-to-transport.svg) |

Оформление C4 повторяет учебные схемы для слайдов (фигуры C4 draw.io, сегменты сети, подписи «запрос / Ответ: /
[интеграция]»), диаграмма последовательности — оформление StarUML (рамка `sd`, фрагменты `alt`, `par`, `critical`).
Исходник — описания в [`tools/docs-diagrams/c4.py`](../../../tools/docs-diagrams/c4.py) и
[`tools/docs-diagrams/sequence.py`](../../../tools/docs-diagrams/sequence.py); `task docs:c4` перегенерирует файлы
без сети и зависимостей, кроме Python. Это исключение из правила «SVG не коммитятся»: README показывает их напрямую.
Файлы `.drawio.svg` содержат модель draw.io и открываются в нём для ручной правки; после правки исходник нужно
обновить, иначе следующая генерация её перезапишет.

## Чем открывать

- **Mermaid** рендерит GitHub прямо в Markdown. Локально: предпросмотр Markdown с поддержкой Mermaid в редакторе
  или [mermaid.live](https://mermaid.live) (вставить блок кода).
- **BPMN**: [demo.bpmn.io](https://demo.bpmn.io) (открыть файл перетаскиванием) или Camunda Modeler. В файлах есть
  DI-разметка (`bpmndi`), поэтому раскладка, дорожки и подписи сохраняются. Модели описательные
  (`isExecutable="false"`): их не исполняет движок процессов, процесс исполняет код сервисов.

## Экспорт в SVG

`task docs:diagrams` экспортирует Mermaid и четыре BPMN в `dist/diagrams/`, с перечнем в `index.md`.
Нужны Python, Node.js 22+ и доступ к npm для Mermaid CLI, bpmn-js и Chromium. Приложение и тесты не запускаются.
Исходники и SVG связаны через `manifest.json`; в CI результат сохраняется артефактом `architecture-svg`.
Локально подтверждены извлечение исходников и синтаксис инструмента; браузерный экспорт ещё не выполнялся.

- Mermaid → SVG: `@mermaid-js/mermaid-cli` (`mmdc -i <файл>.md -o <файл>.svg`);
- BPMN → SVG: bpmn-js открывает исходный XML с DI и выполняет `saveSVG` через Chromium;
  предупреждения импорта завершают экспорт ошибкой. Альтернатива — экспорт вручную из bpmn.io или Camunda Modeler.

Производные SVG этого экспорта не коммитятся (исключение — схемы для README выше): для показа скачивается артефакт CI или используется локальный каталог `dist/diagrams/`.
Пока экспорт не выполнен, смотреть схемы нужно по исходникам способами выше.

## Проверка

- BPMN: `python tools/docs-diagrams/check-bpmn.py` статически проверяет XML, уникальность id, ссылки,
  вызываемые процессы и наличие фигур/рёбер DI. В CI это делает `docs.yml` (см. [testing.md](../testing.md));
  затем bpmn-js дополнительно проверяет импорт при экспорте.
- Mermaid: рендер `mmdc` в `docs.yml`.
- Схемы сверяются с кодом при изменении процесса: переходы O07 — с `services/transfer/internal/domain`, состояния
  заказа перевозки — с `services/logistics-gateway/internal/domain` и миграцией `transport_request`.
