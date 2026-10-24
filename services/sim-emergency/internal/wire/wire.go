// Package wire — тело сообщения ИНТ-02 «агрегированный прогноз ЧС», как его отправляет имитатор C02.
// Имена полей совпадают с белым списком приёма в planning (docs/architecture/security.md, NFR010);
// окончательную схему задаёт контракт ИНТ-02 в libs/contracts, имитатор подстроится под неё.
//
// Сведений о пострадавших в прогнозе нет. Режим InjectPII нарочно добавляет запрещённые поля
// (snils, full_name, patients[]) с заведомо ненастоящими значениями — для проверки, что planning
// отбрасывает их и не сохраняет.
package wire

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
)

// Inject — что подмешать в тело для проверки белого списка.
type Inject string

const (
	// InjectNone — честный прогноз без лишних полей.
	InjectNone Inject = ""
	// InjectPII — добавить поля персональных данных (для теста NFR010 в planning).
	InjectPII Inject = "pii"
)

// PIIFields — поля, которые добавляет InjectPII и которых не должно быть ни в БД, ни в логах planning.
var PIIFields = []string{"snils", "full_name", "patients"}

// ParseInject разбирает параметр inject: пусто или none — ничего, pii — поля ПДн.
func ParseInject(s string) (Inject, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none":
		return InjectNone, nil
	case "pii":
		return InjectPII, nil
	default:
		return "", fmt.Errorf("неизвестное значение inject %q: допустимо pii или пусто", s)
	}
}

// Payload — прогноз ЧС в формате ИНТ-02.
type Payload struct {
	ScenarioVersion string    `json:"scenario_version"`
	ScenarioKind    string    `json:"scenario_kind"`
	ScenarioName    string    `json:"scenario_name"`
	Territory       Territory `json:"territory"`
	PeriodStart     time.Time `json:"period_start"`
	PeriodEnd       time.Time `json:"period_end"`
	Lines           []Line    `json:"lines"`
	// Demo — прогноз синтетический (ADR-0010).
	Demo bool `json:"demo"`
}

// Territory — затронутая территория: районы из libs/refdata с весами.
type Territory struct {
	Name      string     `json:"name"`
	Districts []District `json:"districts"`
}

// District — район территории.
type District struct {
	DistrictCode string  `json:"district_code"`
	Weight       float64 `json:"weight"`
}

// Line — строка прогноза. Группа, принимающая клиника и доля необязательны (FR022, FR040).
type Line struct {
	ComponentKind string  `json:"component_kind"`
	BloodGroup    string  `json:"blood_group,omitempty"`
	VolumeML      int64   `json:"volume_ml"`
	ClinicCode    string  `json:"clinic_code,omitempty"`
	Share         float64 `json:"share,omitempty"`
}

// FromForecast переводит сгенерированный прогноз в формат ИНТ-02.
func FromForecast(f *testdata.Forecast) Payload {
	p := Payload{
		ScenarioVersion: f.ScenarioVersion, ScenarioKind: f.ScenarioKind, ScenarioName: f.ScenarioName,
		Territory:   Territory{Name: f.Territory.Name, Districts: make([]District, 0, len(f.Territory.Districts))},
		PeriodStart: f.PeriodStart.UTC(), PeriodEnd: f.PeriodEnd.UTC(), Demo: f.Demo,
		Lines: make([]Line, 0, len(f.Lines)),
	}
	for _, d := range f.Territory.Districts {
		p.Territory.Districts = append(p.Territory.Districts, District{DistrictCode: d.DistrictCode, Weight: d.Weight})
	}
	for _, l := range f.Lines {
		p.Lines = append(p.Lines, Line{ComponentKind: l.ComponentKind, BloodGroup: l.BloodGroup, VolumeML: l.VolumeML,
			ClinicCode: l.ClinicCode, Share: l.Share})
	}
	return p
}

// fakePatient — заведомо ненастоящие данные: нули в СНИЛС и пометка «ДЕМО» в имени.
type fakePatient struct {
	FullName  string `json:"full_name"`
	SNILS     string `json:"snils"`
	BirthDate string `json:"birth_date"`
}

// Body кодирует прогноз; при InjectPII добавляет поля ПДн на верхний уровень. Кодирование
// детерминировано: поля структуры в порядке объявления, ключи карты — по алфавиту.
func Body(f *testdata.Forecast, inject Inject) ([]byte, error) {
	b, err := json.Marshal(FromForecast(f))
	if err != nil {
		return nil, fmt.Errorf("кодирование прогноза: %w", err)
	}
	if inject != InjectPII {
		return b, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("подмешивание полей: %w", err)
	}
	m["snils"] = "000-000-000 00"
	m["full_name"] = "ДЕМО Ненастоящий Пострадавший"
	m["patients"] = []fakePatient{
		{FullName: "ДЕМО Пострадавший Первый", SNILS: "000-000-000 01", BirthDate: "1900-01-01"},
		{FullName: "ДЕМО Пострадавший Второй", SNILS: "000-000-000 02", BirthDate: "1900-01-02"},
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("кодирование прогноза с полями ПДн: %w", err)
	}
	return out, nil
}
