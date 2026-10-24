package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrInvalidInput — вход расчёта нарушает инварианты (неизвестный вид, повтор ключа, отрицательный объём).
// Приложение отвечает на неё ошибкой запроса, а не частичным планом.
var ErrInvalidInput = errors.New("некорректный вход расчёта плана")

// Статусы единицы компонента крови, которые различает планирование. Код статуса — из справочника
// unit_status_dict сервиса inventory; предложить к передаче можно только единицу «в запасе».
// Единица «на карантине» недоступна: ПП РФ № 641, п. 62 а (статусы единиц в базе), п. 52–53 (карантин плазмы).
const (
	UnitStatusInStock    = "IN_STOCK"
	UnitStatusQuarantine = "QUARANTINE"
)

// Статусы обязательства O07 (docs/domain/obligation-states.md), которые учитывает планирование.
const (
	ObligationPending            = "PENDING"
	ObligationConfirmed          = "CONFIRMED"
	ObligationTransportRequested = "TRANSPORT_REQUESTED"
	ObligationDispatched         = "DISPATCHED"
)

// Unit — единица компонента крови из проекции запаса (снимок inventory с водяным знаком).
type Unit struct {
	UnitID    uuid.UUID
	ClinicID  int64
	Kind      string
	Group     string
	VolumeML  int64
	ExpiresAt time.Time
	Status    string
}

// Kind — вид компонента: типовой объём единицы нужен для пересчёта защищённого объёма в единицы,
// признак группы — для разбиения потребности по AB0/Rh (у криопреципитата группы нет).
type Kind struct {
	Code            string
	TypicalVolumeML int64
	GroupApplicable bool
}

// GroupShare — доля группы AB0/Rh в населении (refdata, демо). Используется, когда группа
// потребности не определена: норматив клиники и неопределённая часть прогноза (FR040).
type GroupShare struct {
	Group string
	Share float64
}

// Norm — действующий норматив обязательного запаса клиники по виду (FR012, проекция inventory).
type Norm struct {
	ClinicID int64
	Kind     string
	NormML   float64
}

// Background — фоновая потребность клиники за период планирования (опубликованная версия demand, FR019–FR021).
// Group пустая — группа не задана, часть делится по долям групп.
type Background struct {
	ClinicID int64
	Kind     string
	Group    string
	VolumeML float64
}

// DistrictWeight — район затронутой территории прогноза ЧС и его вес (население); переводит
// территорию прогноза в районы делает приложение.
type DistrictWeight struct {
	DistrictID int64
	Weight     float64
}

// ClinicShare — доля распределения «район → клиника» w(r, c) из ФП2 (FR046).
type ClinicShare struct {
	DistrictID int64
	ClinicID   int64
	Share      float64
}

// Forecast — агрегированный прогноз ЧС (копия O14, ИНТ-02) после белого списка полей (NFR010).
type Forecast struct {
	ForecastID      uuid.UUID
	ScenarioVersion string
	Territory       []DistrictWeight
	Lines           []ForecastLine
}

// ForecastLine — строка прогноза: вид × группа × объём; ClinicID = 0 — принимающая клиника не указана,
// строка распределяется по долям района с пометкой «грубая оценка» (FR024). Group пустая — неопределённая
// часть (FR040). Объём уже с учётом коррекции оператора, если она была.
type ForecastLine struct {
	LineID   uuid.UUID
	ClinicID int64
	Kind     string
	Group    string
	VolumeML float64
}

// Obligation — обязательство на передачу из проекции transfer.events. Объём в мл пересчитывает
// приложение из числа единиц обязательства.
type Obligation struct {
	ObligationID   uuid.UUID
	SourceClinicID int64
	TargetClinicID int64
	Kind           string
	Group          string
	VolumeML       int64
	// Units — число единиц обязательства; если UnitIDs заданы, должно с ними совпадать или быть 0.
	Units int
	// UnitIDs — закреплённые единицы (TransferConfirmed.units[]). Они остаются в запасе источника
	// (учёт в мл и единицах), но к передаче не предлагаются: годность и объяснение не опираются на них.
	UnitIDs []uuid.UUID
	Status  string
}

// DeliveryEstimate — оценка времени доставки «источник → получатель» из проекции logistics.events (ADR-0014).
type DeliveryEstimate struct {
	SourceClinicID int64
	TargetClinicID int64
	Minutes        int
	EstimatedAt    time.Time
}

// InputVersions — версии входов, по которым план воспроизводится (FR035, NFR009).
type InputVersions struct {
	DemandEstimateID uuid.UUID
	DemandVersionNo  int
	NormVersion      int64
	StockWatermark   int64
	StockSyncedAt    time.Time
	RefdataVersion   int
	// ObligationsWatermark — позиция проекции обязательств (transfer.events), по которой взяты Obligations.
	ObligationsWatermark int64
	// DeliveryEstimatesVersion — версия проекции оценок доставки (logistics.events); время самой старой
	// использованной оценки план считает сам (PlanInputs.OldestEstimateAt).
	DeliveryEstimatesVersion int64
}

// Верхние границы входных величин: за ними — ошибка данных, а не ЧС; заодно исключают переполнение int64
// при суммировании.
const (
	maxVolumeML      = 1e9 // 1 000 м³ — заведомо больше любой потребности города
	maxUnitVolumeML  = 5_000
	maxWeight        = 1e12 // вес района — население или доля
	maxShare         = 1.0
	maxDeliveryMins  = 7 * 24 * 60
	maxObligationQty = 10_000
)

// Params — параметры расчёта. Все значения демонстрационные (ADR-0010), заказчик их не задавал.
type Params struct {
	// CoverageThreshold — порог полноты плана NFR014 (учебные 0,9, допущение А5).
	CoverageThreshold float64
	// MinShelfLifeAtArrival — минимальный остаток срока годности единицы на момент прибытия к получателю (FR015).
	MinShelfLifeAtArrival time.Duration
	// MaxFallbackSources — сколько запасных источников передать в transfer для эскалации.
	MaxFallbackSources int
	// EstimateStaleAfter — возраст оценки доставки, после которого она помечается «устарела» (ADR-0014).
	EstimateStaleAfter time.Duration
}

// DefaultParams — демонстрационные параметры (ADR-0010).
func DefaultParams() Params {
	return Params{
		CoverageThreshold:     0.9,
		MinShelfLifeAtArrival: 24 * time.Hour,
		MaxFallbackSources:    3,
		EstimateStaleAfter:    6 * time.Hour,
	}
}

// Input — всё, что нужно расчёту плана. Порядок элементов в срезах на результат не влияет.
type Input struct {
	PlanID       uuid.UUID
	Versions     InputVersions
	Kinds        []Kind
	GroupShares  []GroupShare
	Norms        []Norm
	Background   []Background
	Forecast     *Forecast
	ClinicShares []ClinicShare
	Units        []Unit
	Obligations  []Obligation
	Delivery     []DeliveryEstimate
	Params       Params
}

// EmergencySource — откуда взята доля сценарной потребности клиники (FR024).
type EmergencySource string

const (
	// SourceForecastC02 — принимающая клиника указана в прогнозе C02.
	SourceForecastC02 EmergencySource = "C02_FORECAST"
	// SourceDistrictShares — доли «район → клиника» ФП2, грубая оценка.
	SourceDistrictShares EmergencySource = "DISTRICT_SHARES"
)

// EmergencyAllocation — сценарная потребность одной строки прогноза, отнесённая к клинике и группе.
type EmergencyAllocation struct {
	LineID            uuid.UUID
	ClinicID          int64
	Kind              string
	Group             string
	VolumeML          int64
	Source            EmergencySource
	IsRoughEstimate   bool
	GroupUndetermined bool
}
