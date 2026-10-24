// Package refdata — общие справочники (схема ref ПР6): клиники, районы, виды компонентов,
// группы крови, срочность, статусы единицы. Статический набор одинаков у всех сервисов и сидируется в базу
// каждого при старте (ADR-0013). Данные синтетические (ADR-0010).
package refdata

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"

	"github.com/jackc/pgx/v5"
)

//go:embed data/*.json
var dataFiles embed.FS

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations — DDL таблиц ref_* (версии 00002–00003).
var Migrations fs.FS = mustSub(migrationFiles, "migrations")

func mustSub(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

// Clinic — клиническая организация.
type Clinic struct {
	ClinicID      int64  `json:"clinic_id"`
	ClinicCode    string `json:"clinic_code"`
	ClinicName    string `json:"clinic_name"`
	DistrictID    int64  `json:"district_id"`
	IsActive      bool   `json:"is_active"`
	IsBloodCenter bool   `json:"is_blood_center"`
}

// District — район города.
type District struct {
	DistrictID   int64  `json:"district_id"`
	DistrictCode string `json:"district_code"`
	DistrictName string `json:"district_name"`
	Population   int64  `json:"population"`
}

// ComponentKind — вид компонента крови.
type ComponentKind struct {
	Code              string `json:"component_kind_code"`
	Name              string `json:"component_kind_name"`
	IsGroupApplicable bool   `json:"is_group_applicable"`
	TypicalVolumeML   int64  `json:"typical_volume_ml"`
	ShelfLifeDays     int    `json:"shelf_life_days"`
	IsActive          bool   `json:"is_active"`
}

// BloodGroup — группа AB0/Rh.
type BloodGroup struct {
	Code            string  `json:"blood_group_code"`
	ABO             string  `json:"abo_group"`
	RhPositive      bool    `json:"rh_positive"`
	PopulationShare float64 `json:"population_share"`
}

// Urgency — срочность заявки и задания; задаёт срок подтверждения (FR049).
type Urgency struct {
	Code                 string `json:"urgency_code"`
	Name                 string `json:"urgency_name"`
	ConfirmWindowMinutes int    `json:"confirm_window_minutes"`
}

// UnitStatus — статус единицы компонента крови. IsAvailable — единицу можно закрепить и передать.
// Статусы единицы в базе, в том числе «на карантине» (ПП РФ № 641, п. 62 а);
// закрепляется только IN_STOCK — решение проекта (ADR-0027).
type UnitStatus struct {
	Code        string `json:"unit_status_code"`
	Name        string `json:"unit_status_name"`
	IsAvailable bool   `json:"is_available"`
}

// Set — весь набор справочников.
type Set struct {
	Version        int
	Districts      []District
	Clinics        []Clinic
	ComponentKinds []ComponentKind
	BloodGroups    []BloodGroup
	Urgency        []Urgency
	UnitStatuses   []UnitStatus
}

type clinicsFile struct {
	Version   int        `json:"version"`
	Districts []District `json:"districts"`
	Clinics   []Clinic   `json:"clinics"`
}

type dictsFile struct {
	Version        int             `json:"version"`
	ComponentKinds []ComponentKind `json:"component_kinds"`
	BloodGroups    []BloodGroup    `json:"blood_groups"`
	Urgency        []Urgency       `json:"urgency"`
	UnitStatuses   []UnitStatus    `json:"unit_statuses"`
}

// Load читает встроенный набор и проверяет его целостность.
func Load() (*Set, error) {
	var cf clinicsFile
	if err := readJSON("data/clinics.json", &cf); err != nil {
		return nil, err
	}
	var df dictsFile
	if err := readJSON("data/dicts.json", &df); err != nil {
		return nil, err
	}
	if cf.Version != df.Version {
		return nil, fmt.Errorf("версии файлов справочников не совпадают: %d и %d", cf.Version, df.Version)
	}
	s := &Set{Version: cf.Version, Districts: cf.Districts, Clinics: cf.Clinics,
		ComponentKinds: df.ComponentKinds, BloodGroups: df.BloodGroups, Urgency: df.Urgency, UnitStatuses: df.UnitStatuses}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return s, nil
}

// MustLoad — Load с panic: справочники встроены, их отсутствие — ошибка сборки.
func MustLoad() *Set {
	s, err := Load()
	if err != nil {
		panic(err)
	}
	return s
}

func readJSON(name string, v any) error {
	b, err := dataFiles.ReadFile(name)
	if err != nil {
		return fmt.Errorf("чтение %s: %w", name, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("разбор %s: %w", name, err)
	}
	return nil
}

// Validate проверяет уникальность кодов, ссылки на районы и сумму долей групп.
func (s *Set) Validate() error {
	districts := map[int64]bool{}
	codes := map[string]bool{}
	for _, d := range s.Districts {
		if districts[d.DistrictID] || codes[d.DistrictCode] {
			return fmt.Errorf("район %d/%s повторяется", d.DistrictID, d.DistrictCode)
		}
		if d.Population <= 0 {
			return fmt.Errorf("район %s без населения", d.DistrictCode)
		}
		districts[d.DistrictID] = true
		codes[d.DistrictCode] = true
	}
	clinicIDs := map[int64]bool{}
	clinicCodes := map[string]bool{}
	for _, c := range s.Clinics {
		if clinicIDs[c.ClinicID] || clinicCodes[c.ClinicCode] {
			return fmt.Errorf("клиника %d/%s повторяется", c.ClinicID, c.ClinicCode)
		}
		if !districts[c.DistrictID] {
			return fmt.Errorf("клиника %s ссылается на неизвестный район %d", c.ClinicCode, c.DistrictID)
		}
		clinicIDs[c.ClinicID] = true
		clinicCodes[c.ClinicCode] = true
	}
	kinds := map[string]bool{}
	for _, k := range s.ComponentKinds {
		if kinds[k.Code] {
			return fmt.Errorf("вид %s повторяется", k.Code)
		}
		if k.TypicalVolumeML <= 0 || k.ShelfLifeDays <= 0 {
			return fmt.Errorf("вид %s: объём и срок годности должны быть положительными", k.Code)
		}
		kinds[k.Code] = true
	}
	var share float64
	groups := map[string]bool{}
	for _, g := range s.BloodGroups {
		if groups[g.Code] {
			return fmt.Errorf("группа %s повторяется", g.Code)
		}
		if g.ABO != "O" && g.ABO != "A" && g.ABO != "B" && g.ABO != "AB" {
			return fmt.Errorf("группа %s: недопустимая AB0 %q", g.Code, g.ABO)
		}
		groups[g.Code] = true
		share += g.PopulationShare
	}
	if len(s.BloodGroups) != 8 || math.Abs(share-1) > 1e-6 {
		return fmt.Errorf("групп крови %d, сумма долей %.4f: ожидали 8 и 1.0", len(s.BloodGroups), share)
	}
	urg := map[string]bool{}
	for _, u := range s.Urgency {
		if urg[u.Code] || u.ConfirmWindowMinutes <= 0 {
			return fmt.Errorf("срочность %s некорректна", u.Code)
		}
		urg[u.Code] = true
	}
	statuses := map[string]bool{}
	available := 0
	for _, st := range s.UnitStatuses {
		if st.Code == "" || st.Name == "" || statuses[st.Code] {
			return fmt.Errorf("статус единицы %q пустой или повторяется", st.Code)
		}
		statuses[st.Code] = true
		if st.IsAvailable {
			available++
		}
	}
	if len(s.UnitStatuses) > 0 && available == 0 {
		return errors.New("ни один статус единицы не отмечен доступным: закреплять было бы нечего")
	}
	return nil
}

// UnitStatus находит статус единицы.
func (s *Set) UnitStatus(code string) (UnitStatus, bool) {
	for _, st := range s.UnitStatuses {
		if st.Code == code {
			return st, true
		}
	}
	return UnitStatus{}, false
}

// IsUnitAvailable сообщает, можно ли закрепить единицу в этом статусе. Неизвестный статус — недоступна.
func (s *Set) IsUnitAvailable(code string) bool {
	st, ok := s.UnitStatus(code)
	return ok && st.IsAvailable
}

// Clinic находит клинику по идентификатору.
func (s *Set) Clinic(id int64) (Clinic, bool) {
	for _, c := range s.Clinics {
		if c.ClinicID == id {
			return c, true
		}
	}
	return Clinic{}, false
}

// Kind находит вид компонента.
func (s *Set) Kind(code string) (ComponentKind, bool) {
	for _, k := range s.ComponentKinds {
		if k.Code == code {
			return k, true
		}
	}
	return ComponentKind{}, false
}

// UrgencyByCode находит срочность.
func (s *Set) UrgencyByCode(code string) (Urgency, bool) {
	for _, u := range s.Urgency {
		if u.Code == code {
			return u, true
		}
	}
	return Urgency{}, false
}

// Seed записывает набор в таблицы ref_* идемпотентно (UPSERT) и фиксирует версию в refdata_version.
// Вызывается сервисом после миграций. Повторный вызов с той же версией ничего не меняет.
func (s *Set) Seed(ctx context.Context, tx pgx.Tx) error {
	var current *int
	if err := tx.QueryRow(ctx, `SELECT max(version) FROM refdata_version`).Scan(&current); err != nil {
		return fmt.Errorf("версия справочников: %w", err)
	}
	if current != nil && *current >= s.Version {
		return nil
	}
	b := &pgx.Batch{}
	for _, d := range s.Districts {
		b.Queue(`INSERT INTO ref_district (district_id, district_code, district_name, population) VALUES ($1,$2,$3,$4)
			ON CONFLICT (district_id) DO UPDATE SET district_code = EXCLUDED.district_code, district_name = EXCLUDED.district_name, population = EXCLUDED.population`,
			d.DistrictID, d.DistrictCode, d.DistrictName, d.Population)
	}
	for _, c := range s.Clinics {
		b.Queue(`INSERT INTO ref_clinic (clinic_id, clinic_code, clinic_name, district_id, is_active, is_blood_center) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (clinic_id) DO UPDATE SET clinic_code = EXCLUDED.clinic_code, clinic_name = EXCLUDED.clinic_name, district_id = EXCLUDED.district_id, is_active = EXCLUDED.is_active, is_blood_center = EXCLUDED.is_blood_center`,
			c.ClinicID, c.ClinicCode, c.ClinicName, c.DistrictID, c.IsActive, c.IsBloodCenter)
	}
	for _, k := range s.ComponentKinds {
		b.Queue(`INSERT INTO ref_component_kind (component_kind_code, component_kind_name, is_group_applicable, typical_volume_ml, shelf_life_days, is_active) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (component_kind_code) DO UPDATE SET component_kind_name = EXCLUDED.component_kind_name, is_group_applicable = EXCLUDED.is_group_applicable, typical_volume_ml = EXCLUDED.typical_volume_ml, shelf_life_days = EXCLUDED.shelf_life_days, is_active = EXCLUDED.is_active`,
			k.Code, k.Name, k.IsGroupApplicable, k.TypicalVolumeML, k.ShelfLifeDays, k.IsActive)
	}
	for _, g := range s.BloodGroups {
		b.Queue(`INSERT INTO ref_blood_group (blood_group_code, abo_group, rh_positive, population_share) VALUES ($1,$2,$3,$4)
			ON CONFLICT (blood_group_code) DO UPDATE SET abo_group = EXCLUDED.abo_group, rh_positive = EXCLUDED.rh_positive, population_share = EXCLUDED.population_share`,
			g.Code, g.ABO, g.RhPositive, g.PopulationShare)
	}
	for _, u := range s.Urgency {
		b.Queue(`INSERT INTO ref_urgency (urgency_code, urgency_name, confirm_window_minutes) VALUES ($1,$2,$3)
			ON CONFLICT (urgency_code) DO UPDATE SET urgency_name = EXCLUDED.urgency_name, confirm_window_minutes = EXCLUDED.confirm_window_minutes`,
			u.Code, u.Name, u.ConfirmWindowMinutes)
	}
	for _, st := range s.UnitStatuses {
		b.Queue(`INSERT INTO ref_unit_status (unit_status_code, unit_status_name, is_available) VALUES ($1,$2,$3)
			ON CONFLICT (unit_status_code) DO UPDATE SET unit_status_name = EXCLUDED.unit_status_name, is_available = EXCLUDED.is_available`,
			st.Code, st.Name, st.IsAvailable)
	}
	b.Queue(`INSERT INTO refdata_version (version, applied_at) VALUES ($1, now())`, s.Version)
	res := tx.SendBatch(ctx, b)
	defer func() { _ = res.Close() }()
	for range b.Len() {
		if _, err := res.Exec(); err != nil {
			return fmt.Errorf("сид справочников: %w", err)
		}
	}
	return nil
}
