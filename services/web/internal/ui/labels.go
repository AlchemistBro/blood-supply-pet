package ui

import "fmt"

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

func label(m map[string]string, v any) string {
	code := fmt.Sprint(v)
	if s, ok := m[code]; ok {
		return s
	}
	return code
}

func labelFuncs() map[string]any {
	return map[string]any{
		"kindLabel":    func(v any) string { return label(kindLabels, v) },
		"urgencyLabel": func(v any) string { return label(urgencyLabels, v) },
		"auditModule":  func(v any) string { return label(auditModuleLabels, v) },
		"auditAction":  func(v any) string { return label(auditActionLabels, v) },
		"auditObject":  func(v any) string { return label(auditObjectLabels, v) },
		"auditResult":  func(v any) string { return label(auditResultLabels, v) },
	}
}
