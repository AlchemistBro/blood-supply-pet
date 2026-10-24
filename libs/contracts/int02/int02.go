// Package int02 — входящий контракт ИНТ-02: агрегированный прогноз ЧС от C02 (POST /int/forecast в planning).
// Это тело HTTP-запроса, а не сообщение Kafka: тип регистрируется в contracts.Inbound. Форма совпадает с телом
// имитатора sim-emergency (services/sim-emergency/internal/wire).
//
// Белый список полей (NFR010, docs/architecture/security.md): схема описывает ровно то, что planning принимает и
// хранит, и запрещает остальное (additionalProperties: false на каждом уровне). Сведения о пострадавших — ФИО,
// СНИЛС, списки пациентов — схема не описывает: входной адаптер отбрасывает их до проверки и не сохраняет.
package int02

import "time"

// EmergencyForecast — агрегированный прогноз ЧС: сценарий, территория, период и строки потребности.
// Принимающая клиника (clinic_code) и доля в строке необязательны: без них распределение идёт по долям ФП2
// с пометкой «грубая оценка» (FR022, FR024); без группы — неопределённая часть (FR040).
type EmergencyForecast struct {
	ScenarioVersion string         `json:"scenario_version"`
	ScenarioKind    string         `json:"scenario_kind,omitempty"`
	ScenarioName    string         `json:"scenario_name,omitempty"`
	Demo            *bool          `json:"demo,omitempty"`
	Territory       Territory      `json:"territory"`
	PeriodStart     time.Time      `json:"period_start"`
	PeriodEnd       time.Time      `json:"period_end"`
	Lines           []ForecastLine `json:"lines"`
}

// Territory — затронутая территория: название и районы из справочника с весами.
type Territory struct {
	Name      string     `json:"name"`
	Districts []District `json:"districts,omitempty"`
}

// ForecastLine — строка прогноза: вид, группа (пусто — неопределённая часть), объём в мл,
// принимающая клиника (ref_clinic.clinic_code) и её доля — если C02 их знает.
type ForecastLine struct {
	ClinicCode    string   `json:"clinic_code,omitempty"`
	ComponentKind string   `json:"component_kind"`
	BloodGroup    string   `json:"blood_group,omitempty"`
	VolumeML      int64    `json:"volume_ml"`
	Share         *float64 `json:"share,omitempty"`
}

// District — район территории (ref_district.district_code) и его вес в сценарии.
type District struct {
	DistrictCode string  `json:"district_code"`
	Weight       float64 `json:"weight"`
}
