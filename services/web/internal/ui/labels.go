package ui

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Русские подписи для кодов, которые приходят из сервисов. Неизвестный код показывается как есть:
// новая версия сервиса не должна скрывать данные из-за устаревшего словаря интерфейса.

var kindLabels = map[string]string{
	"RBC":  "Эритроцитная масса",
	"FFP":  "Свежезамороженная плазма",
	"PLT":  "Тромбоцитный концентрат",
	"CRYO": "Криопреципитат",
}

var urgencyLabels = map[string]string{
	"PLANNED":   "Плановая",
	"URGENT":    "Срочная",
	"EMERGENCY": "Экстренная",
}

var auditModuleLabels = map[string]string{
	"web": "Веб-интерфейс", "inventory": "Запас", "demand": "Потребность", "planning": "План пополнения",
	"matching": "Подбор", "transfer": "Передачи", "logistics": "Перевозка", "notification": "Уведомления",
	"audit": "Аудит",
}

var auditActionLabels = map[string]string{
	"SYNC_COMPLETED": "Синхронизация с АИСТ-М завершена", "SNAPSHOT_DISCREPANCY": "Расхождение при сверке запаса",
	"CALCULATE_STOCK_NORM": "Расчёт норматива запаса", "NOTIFICATION_SENT": "Уведомление отправлено",
	"NOTIFICATION_QUEUED": "Уведомление поставлено в очередь", "ACCESS_DENIED": "Отказ в доступе",
	"LOGIN": "Вход", "LOGOUT": "Выход", "OPENED": "Задание открыто", "EXPIRED": "Срок подтверждения истёк",
	"FILE_UPLOADED": "Файл статистики загружен", "FILE_REJECTED": "Файл статистики отклонён",
	"ESTIMATE_CALCULATED": "Оценка потребности рассчитана", "ESTIMATE_PUBLISHED": "Оценка потребности опубликована",
	"CLINICAL_REQUEST_CREATED": "Заявка создана", "READ_PATIENT_CASE": "Чтение номера случая пациента",
	"VARIANT_SELECTED": "Вариант выбран", "FORECAST_ACCEPTED": "Прогноз ЧС принят", "PLAN_BUILT": "План построен",
	"DISPATCHED": "Отправлено", "CONFIRMED": "Передача подтверждена", "DELIVERED": "Доставлено",
	"TRANSPORT_REQUESTED": "Перевозка запрошена", "REQUEST_ACCEPTED": "Заказ принят перевозчиком",
	"TRANSPORT_ACCEPTED": "Перевозка принята", "PATIENT_ALLOCATION_DELIVERED": "Доставка пациенту учтена",
	"AUDIT_EXPORT": "Выгрузка журнала",
}

var auditObjectLabels = map[string]string{
	"sync_run": "Сеанс синхронизации", "unit": "Единица", "stock_norm": "Норматив", "notification": "Уведомление",
	"obligation": "Задание", "operation": "Операция", "session": "Сеанс", "statistics_file": "Файл статистики",
	"demand_estimate": "Оценка потребности", "clinical_request": "Заявка", "emergency_forecast": "Прогноз ЧС",
	"replenishment_plan": "План пополнения", "transfer_obligation": "Задание на передачу", "audit_record": "Журнал аудита",
}

var auditResultLabels = map[string]string{"SUCCESS": "Успешно", "DENIED": "Отказано", "FAILURE": "Ошибка"}

var codeLabels = map[string]string{
	// файлы и оценки потребности
	"PUBLISHED": "Опубликована", "CALCULATED": "Рассчитана", "ACCEPTED": "Принято", "PARTIAL": "Принято частично",
	"REJECTED": "Отклонено", "DISTRICT_PROFILE": "Профиль районов", "CONSUMPTION_HISTORY": "История расхода",
	"CLINIC_SHARE": "Доли район → клиника", "FILE": "Файл", "C01": "АИСТ-М",
	// единицы
	"IN_STOCK": "В запасе", "QUARANTINE": "Карантин", "ISSUED": "Выдана", "TRANSFERRED": "Передана", "DISCARDED": "Списана",
	// очереди и письма
	"PENDING": "В очереди", "SENT": "Отправлено", "FAILED": "Ошибка", "REQUESTING": "Запрашивается", "UNKNOWN": "Неизвестно",
	"CANCELLED": "Отменено", "SUCCESS": "Успешно", "ERROR": "Ошибка", "DENIED": "Отказано",
	"CLINIC_OPERATOR": "Оператор клиники", "DUTY_OFFICER": "Дежурный смены", "PHYSICIAN": "Врач", "IT_SERVICE": "ИТ-служба",
	"URGENT_TASK": "Срочное задание", "ESCALATION": "Эскалация", "TRANSFER_BROKEN": "Передача нарушена",
	"TRANSFER_TERMINAL": "Передача завершена", "STOCK_BELOW_NORM": "Запас ниже норматива", "SECURITY_INCIDENT": "Инцидент безопасности",
	// сеансы синхронизации
	"CHANGES": "Изменения", "DAILY_SNAPSHOT": "Суточный снимок", "ON_DEMAND": "По запросу", "UNIT_CHECK": "Проверка единицы",
	// подбор и план
	"ESTIMATED": "по оценке", "PLAN": "План", "REQUEST": "Заявка врача", "DOCTOR_CHOICE": "Выбор врача", "PLAN_PROPOSAL": "Предложение плана",
	"NO_SOURCE": "Нет подходящего источника", "BELOW_UNIT_REMAINDER": "Остаток меньше одной единицы", "DISTRICT_SHARES": "Доли районов",
	"BLOOD_GROUP": "Группа крови", "PHENOTYPE": "Фенотип", "EXPIRY": "Срок годности", "KIND": "Вид компонента", "SOURCE": "Источник",
	"SOURCE_REFUSED": "Источник отказал", "NOT_ALLOCATED": "Не закреплено", "UNKNOWN_UNIT": "Единица неизвестна", "SAME_UNIT": "Та же единица",
	"PLAN_RECALCULATED": "План пересчитан", "REQUEST_CANCELLED": "Заявка отменена", "DUTY_OFFICER_DECISION": "Решение дежурного",
	"RESOLVED": "Проблема решена", "TEMPERATURE": "нарушен температурный режим", "TEMPERATURE_EXCURSION": "Нарушение температурного режима в перевозке",
	"EXPIRED_NO_FALLBACK": "Срок подтверждения истёк, запасных источников нет", "ALWAYS": "Всегда", "NO_ACTION_REQUIRED": "Действий не требуется",
}

// deref разворачивает указатели (nullable-поля ответов сервисов), nil даёт пустую строку.
func deref(v any) string {
	rv := reflect.ValueOf(v)
	for rv.IsValid() && rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return ""
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return ""
	}
	return fmt.Sprint(rv.Interface())
}

func label(m map[string]string, v any) string {
	code := deref(v)
	if s, ok := m[code]; ok {
		return s
	}
	return code
}

var conditionTokens = map[string]string{
	"URGENCY": "срочность", "CONSTRAINT": "у заявки есть ограничение", "NOT_CONSTRAINT": "у заявки нет ограничения",
	"NO_GROUP_SUBSTITUTION": "запрет замены группы", "IRRADIATED": "облучённые единицы", "MIN_REMAINING_HOURS": "минимальная годность",
	"PLANNED": "плановая", "URGENT": "срочная", "EMERGENCY": "экстренная",
}

// ruleCondition переводит условие применения правила («URGENCY=PLANNED; NOT_CONSTRAINT=IRRADIATED») в читаемый текст.
// Неизвестные части остаются как есть.
func ruleCondition(v any) string {
	text := strings.TrimSpace(deref(v))
	if text == "" {
		return "Всегда"
	}
	tr := func(s string) string {
		if t, ok := conditionTokens[s]; ok {
			return t
		}
		if t, ok := codeLabels[s]; ok {
			return t
		}
		return s
	}
	var parts []string
	for _, clause := range strings.Split(text, ";") {
		key, value, found := strings.Cut(strings.TrimSpace(clause), "=")
		if !found {
			parts = append(parts, tr(key))
			continue
		}
		var values []string
		for _, one := range strings.Split(value, "|") {
			values = append(values, tr(one))
		}
		parts = append(parts, tr(key)+": "+strings.Join(values, " или "))
	}
	return strings.Join(parts, "; ")
}

func labelFuncs() map[string]any {
	return map[string]any{
		"kindLabel": func(v any) string {
			if s, ok := kindLabels[deref(v)]; ok {
				return s
			}
			return label(codeLabels, v)
		},
		"urgencyLabel": func(v any) string { return label(urgencyLabels, v) },
		"auditModule":  func(v any) string { return label(auditModuleLabels, v) },
		"auditAction":  func(v any) string { return label(auditActionLabels, v) },
		"auditObject":  func(v any) string { return label(auditObjectLabels, v) },
		"dec":          func(v float64) string { return strings.ReplaceAll(strconv.FormatFloat(v, 'f', -1, 64), ".", ",") },
		"auditModules": func() []string {
			return []string{"web", "matching", "transfer", "logistics", "inventory", "demand", "planning", "notification"}
		},
		"ruleCondition": ruleCondition,
		"code":          func(v any) string { return label(codeLabels, v) },
		"auditResult":   func(v any) string { return label(auditResultLabels, v) },
	}
}
