// Package int02 — входящий контракт ИНТ-02: прогноз ЧС от C02 (POST /int/forecast в planning).
// Это тело HTTP-запроса, а не сообщение Kafka: тип регистрируется в contracts.Inbound.
//
// Белый список полей (NFR010): схема перечисляет ровно те поля, которые planning хранит, и запрещает остальные
// (additionalProperties: false на каждом уровне). Сведения о пострадавших — ФИО, СНИЛС, списки пациентов —
// не принимаются и не сохраняются: входной адаптер отбрасывает всё, чего нет в списке.
package int02

import "time"

// EmergencyForecast — прогноз ЧС: сценарий, территория, период и строки потребности по видам и группам.
// ClinicID в строке — принимающая клиника; нет — распределение по долям ФП2 с пометкой «грубая оценка» (FR024).
type EmergencyForecast struct {
	ScenarioVersion string         `json:"scenario_version"`
	Territory       string         `json:"territory"`
	PeriodStart     time.Time      `json:"period_start"`
	PeriodEnd       time.Time      `json:"period_end"`
	IssuedAt        *time.Time     `json:"issued_at,omitempty"`
	Lines           []ForecastLine `json:"lines"`
}

// ForecastLine — строка прогноза: вид, группа (пусто — неопределённая часть, FR040), объём.
type ForecastLine struct {
	ClinicID         *int64  `json:"clinic_id,omitempty"`
	ComponentKind    string  `json:"component_kind"`
	BloodGroup       string  `json:"blood_group,omitempty"`
	ForecastVolumeML float64 `json:"forecast_volume_ml"`
}
