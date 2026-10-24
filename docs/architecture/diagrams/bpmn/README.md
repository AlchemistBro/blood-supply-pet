# BPMN-схемы

Процессы в нотации BPMN 2.0 с DI-разметкой (`bpmndi`): файлы открываются в [bpmn.io](https://demo.bpmn.io)
и Camunda Modeler. Модели описательные (`isExecutable="false"`): процесс исполняет код сервисов, схема объясняет его.
Перечень всех схем проекта и способы рендера — [../README.md](../README.md).

| Файл | Процесс | Дорожки | Основание |
| --- | --- | --- | --- |
| [confirm-transfer.bpmn](confirm-transfer.bpmn) | Подтверждение передачи: O07 от задания до доставки | Оператор клиники-источника, transfer, inventory → АИСТ-М, logistics-gateway, перевозчик (служба перевозки) | [obligation-states.md](../../../domain/obligation-states.md), [saga-confirmation.md](../../saga-confirmation.md), ADR-0004, ADR-0011, ADR-0022, ADR-0027 |
| [replenishment-plan.bpmn](replenishment-plan.bpmn) | Прогноз ЧС → план пополнения → предложения → обязательства | C02, planning, transfer, оператор клиники-источника | [05-planning.md](../../../tasks/05-planning.md), FR021–FR028, NFR010, NFR013, NFR014 |
| [clinical-request.bpmn](clinical-request.bpmn) | Заявка врача → варианты → выбор → задание источнику → ИНТ-05 | Врач, matching, transfer, оператор источника, inventory → АИСТ-М, notification | [06-matching.md](../../../tasks/06-matching.md), FR029–FR035, FR043, FR044, FR051, FR052 |
| [csv-demand.bpmn](csv-demand.bpmn) | Загрузка CSV → проверка → расчёт → публикация версии оценки | Аналитик, demand, planning | [04-demand.md](../../../tasks/04-demand.md), FR016–FR020, FR045, FR046 |

Соглашения:

- Один пул на процесс, дорожки — участники (роль или сервис). Обмен между сервисами идёт через Kafka; на схеме он
  показан потоком управления между дорожками, а имя задачи называет сообщение (`RequestTransport`, `TransportAccepted`).
- Реакции на события, которые приходят в любой момент (выбытие единицы, отмена, отказ перевозчика), — событийные
  подпроцессы без прерывания основного потока.
- `replenishment-plan` и `clinical-request` вызывают `confirm-transfer` вызывающей активностью (`callActivity`).
- Этапы, которые ещё не реализованы (отправка и доставка, этап 7; ИНТ-05, этап 6), помечены в аннотациях.
