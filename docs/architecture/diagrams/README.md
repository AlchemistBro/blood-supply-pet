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

## Чем открывать

- **Mermaid** рендерит GitHub прямо в Markdown. Локально: предпросмотр Markdown с поддержкой Mermaid в редакторе
  или [mermaid.live](https://mermaid.live) (вставить блок кода).
- **BPMN**: [demo.bpmn.io](https://demo.bpmn.io) (открыть файл перетаскиванием) или Camunda Modeler. В файлах есть
  DI-разметка (`bpmndi`), поэтому раскладка, дорожки и подписи сохраняются. Модели описательные
  (`isExecutable="false"`): их не исполняет движок процессов, процесс исполняет код сервисов.

## Экспорт в SVG

SVG для README и презентаций делается отдельной задачей (этап 9, `task docs:diagrams`), потому что инструменты рендера
загружаются из сети:

- Mermaid → SVG: `@mermaid-js/mermaid-cli` (`mmdc -i <файл>.md -o <файл>.svg`);
- BPMN → изображение: `bpmn-to-image` (headless-браузер, `bpmn-to-image bpmn/<процесс>.bpmn:bpmn/<процесс>.png`)
  или экспорт SVG вручную из demo.bpmn.io и Camunda Modeler.

Готовые SVG кладутся рядом с исходником (`bpmn/<процесс>.svg`, `c4/*.svg`) и перегенерируются при каждом изменении
исходника. Пока экспорта нет, смотреть схемы нужно по исходникам способами выше.

## Проверка

- BPMN: XML разбирается без ошибок (`python -c "import xml.dom.minidom,sys; xml.dom.minidom.parse(sys.argv[1])" <файл>`);
  у каждого элемента процесса есть фигура или ребро в DI, потоки ссылаются на существующие узлы. В CI это делает
  `docs.yml` (см. [testing.md](../testing.md)).
- Mermaid: рендер `mmdc` в `docs.yml`.
- Схемы сверяются с кодом при изменении процесса: переходы O07 — с `services/transfer/internal/domain`, состояния
  заказа перевозки — с `services/logistics-gateway/internal/domain` и миграцией `transport_request`.
