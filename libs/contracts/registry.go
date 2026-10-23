package contracts

import "strconv"

// Registry — все типы сообщений: имя типа → конструктор пустого значения.
// Используется проверкой контрактов (пример → Go-тип → JSON → схема) и consumer-ами для декодирования.
// Заполняется в init() файлов registry_<topic>.go: каждый контекст регистрирует свои типы в своём файле,
// поэтому пакеты контрактов разных этапов не правят один и тот же список.
var Registry = map[string]func() Payload{}

var duplicates []string

// register добавляет типы в Registry под именем MessageType(). Повтор имени не паникует при старте
// процесса, а копится в DuplicateTypes и роняет тест контрактов.
func register(ctors ...func() Payload) {
	for _, ctor := range ctors {
		name := ctor().MessageType()
		if _, dup := Registry[name]; dup {
			duplicates = append(duplicates, name)
		}
		Registry[name] = ctor
	}
}

// DuplicateTypes возвращает имена типов, зарегистрированных больше одного раза (для теста контрактов).
func DuplicateTypes() []string {
	return append([]string(nil), duplicates...)
}

// SchemaPath возвращает путь к JSON Schema типа относительно libs/contracts.
func SchemaPath(topicDir, msgType string, version int) string {
	return "schemas/" + topicDir + "/" + msgType + ".v" + strconv.Itoa(version) + ".json"
}
