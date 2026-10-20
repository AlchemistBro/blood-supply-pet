package contracts

import (
	"strconv"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
)

// Registry — все типы сообщений: имя типа → конструктор пустого значения.
// Используется проверкой контрактов (пример → Go-тип → JSON → схема) и consumer-ами для декодирования.
var Registry = map[string]func() Payload{
	"AuditRecorded": func() Payload { return &audit.AuditRecorded{} },
}

// SchemaPath возвращает путь к JSON Schema типа относительно libs/contracts.
func SchemaPath(topicDir, msgType string, version int) string {
	return "schemas/" + topicDir + "/" + msgType + ".v" + strconv.Itoa(version) + ".json"
}
