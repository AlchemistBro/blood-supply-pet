// Package contracts — опубликованный язык системы: имена топиков, интерфейс полезной нагрузки и
// общие правила. Конкретные события и команды лежат в подпакетах по контексту-владельцу
// (audit, transfer, inventory …) вместе с JSON Schema в schemas/ и примерами в examples/.
//
// Правило совместимости: поле можно добавить (необязательным), нельзя удалить или сменить тип.
// Несовместимое изменение — новый тип с суффиксом версии. Проверяет tools/contracts-check.
package contracts

// Payload — полезная нагрузка сообщения. Тип и версия схемы попадают в заголовки Kafka,
// по ним получатель выбирает обработчик и валидирует тело.
type Payload interface {
	MessageType() string
	SchemaVersion() int
}

// Топики. Владелец топика — сервис, который в него пишет (для команд — тот, кто их исполняет).
const (
	TopicInventoryEvents   = "inventory.events"
	TopicInventoryCommands = "inventory.commands"
	TopicDemandEvents      = "demand.events"
	TopicPlanningEvents    = "planning.events"
	TopicMatchingEvents    = "matching.events"
	TopicTransferCommands  = "transfer.commands"
	TopicTransferEvents    = "transfer.events"
	TopicLogisticsCommands = "logistics.commands"
	TopicLogisticsReplies  = "logistics.replies"
	TopicLogisticsEvents   = "logistics.events"
	TopicAuditEvents       = "audit.events"
)

// AllTopics — полный список для создания при старте стенда.
var AllTopics = []string{
	TopicInventoryEvents, TopicInventoryCommands, TopicDemandEvents, TopicPlanningEvents,
	TopicMatchingEvents, TopicTransferCommands, TopicTransferEvents, TopicLogisticsCommands,
	TopicLogisticsReplies, TopicLogisticsEvents, TopicAuditEvents,
}

// PatientCaseSealedPattern — шаблон зашифрованного конверта номера случая пациента v1.<kid>.<base64url>
// (ADR-0020), формат libs/k1seal. Каждое поле patient_case_sealed в схемах объявлено строкой ровно с этим шаблоном (тест контрактов).
const PatientCaseSealedPattern = `^v1\.[A-Za-z0-9_-]{1,32}\.[A-Za-z0-9_-]+$`

// DLTSuffix — суффикс топика ядовитых сообщений: после исчерпания повторов сообщение уходит в <topic>.dlt.
const DLTSuffix = ".dlt"
