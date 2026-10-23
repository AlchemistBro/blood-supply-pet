package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
)

// Статусы единицы компонента крови по данным АИСТ-М (C01) — коды справочника ref_unit_status,
// те же, что в опубликованном контракте. Доступность статуса задаёт справочник (is_available).
const (
	StatusInStock     = inventory.StatusInStock
	StatusQuarantine  = inventory.StatusQuarantine
	StatusIssued      = inventory.StatusIssued
	StatusDiscarded   = inventory.StatusDiscarded
	StatusTransferred = inventory.StatusTransferred
)

// Причины выбытия (UnitWithdrawn). Истёкший срок годности АИСТ-М присылает как DISCARDED с причиной EXPIRED.
// Трансфузию система не видит (её фиксирует клиника), поэтому выдача — ISSUED.
const (
	ReasonIssued      = inventory.WithdrawIssued
	ReasonDiscarded   = inventory.WithdrawDiscarded
	ReasonTransferred = inventory.WithdrawTransferred
	ReasonExpired     = inventory.WithdrawExpired
)

var (
	// ErrInvalidSource — запись АИСТ-М не проходит проверку и не применяется.
	ErrInvalidSource = errors.New("некорректная запись единицы из АИСТ-М")
	// ErrInvalidProtectedVolume — защищённый объём задан неверно.
	ErrInvalidProtectedVolume = errors.New("некорректный защищённый объём")
)

// KnownStatus сообщает, что статус есть в справочнике.
func KnownStatus(s string) bool {
	switch s {
	case StatusInStock, StatusQuarantine, StatusIssued, StatusDiscarded, StatusTransferred:
		return true
	}
	return false
}

// IsWithdrawn — единица выбыла из наличия: выдана, списана или передана.
func IsWithdrawn(s string) bool {
	return s == StatusIssued || s == StatusDiscarded || s == StatusTransferred
}

func knownReason(r string) bool {
	switch r {
	case ReasonIssued, ReasonDiscarded, ReasonTransferred, ReasonExpired:
		return true
	}
	return false
}

// WithdrawReason — причина выбытия для UnitWithdrawn: из withdrawal_reason АИСТ-М, а если её нет — по статусу.
// Причина важнее статуса: истёкший срок приходит статусом DISCARDED и причиной EXPIRED.
func WithdrawReason(status, withdrawalReason string) string {
	if withdrawalReason != "" {
		return withdrawalReason
	}
	switch status {
	case StatusIssued:
		return ReasonIssued
	case StatusTransferred:
		return ReasonTransferred
	default:
		return ReasonDiscarded
	}
}

// SourceUnit — единица в том виде, как её прислала АИСТ-М, с клиникой и группой, уже сопоставленными
// со справочниками. AistUnitCode хранится в копии, но наружу не публикуется (ADR-0010).
type SourceUnit struct {
	AistUnitCode    string
	ClinicID        int64
	ComponentKind   string
	BloodGroup      string
	RhPositive      bool
	Phenotype       string
	VolumeML        int64
	ExpiresAt       time.Time
	StorageLocation string
	Status          string
	// WithdrawalReason — причина выбытия из АИСТ-М; в копии не хранится, нужна только для события.
	WithdrawalReason string
	// Version — версия записи в АИСТ-М (source_version): растёт с каждым изменением.
	Version   int64
	ChangedAt time.Time
}

// Validate проверяет запись АИСТ-М.
func (s SourceUnit) Validate() error {
	switch {
	case s.AistUnitCode == "":
		return fmt.Errorf("%w: нет кода единицы", ErrInvalidSource)
	case s.Version <= 0:
		return fmt.Errorf("%w: версия %d", ErrInvalidSource, s.Version)
	case !KnownStatus(s.Status):
		return fmt.Errorf("%w: неизвестный статус %q", ErrInvalidSource, s.Status)
	case s.WithdrawalReason != "" && !knownReason(s.WithdrawalReason):
		return fmt.Errorf("%w: неизвестная причина выбытия %q", ErrInvalidSource, s.WithdrawalReason)
	case s.VolumeML < 0:
		return fmt.Errorf("%w: отрицательный объём", ErrInvalidSource)
	case s.ClinicID <= 0 || s.ComponentKind == "" || s.BloodGroup == "":
		return fmt.Errorf("%w: нет клиники, вида или группы", ErrInvalidSource)
	}
	return nil
}

// Unit — единица в копии запаса inventory (O01). UnitID — суррогат uuid v7, единственный идентификатор
// единицы вне inventory. Version — source_version: старая версия не перезаписывает новую.
type Unit struct {
	UnitID uuid.UUID
	SourceUnit
	StatusReceivedAt time.Time
}

// EventKind — какое событие публикует применённое изменение.
type EventKind int

// События об единице.
const (
	EventNone EventKind = iota
	EventRegistered
	EventUpdated
	EventWithdrawn
)

func (k EventKind) String() string {
	switch k {
	case EventRegistered:
		return "UnitRegistered"
	case EventUpdated:
		return "UnitUpdated"
	case EventWithdrawn:
		return "UnitWithdrawn"
	}
	return "none"
}

// Change — результат применения записи АИСТ-М к копии.
type Change struct {
	Event EventKind
	// Unit — состояние после применения (при EventNone — прежнее).
	Unit Unit
	// StatusChanged — статус новый или изменился: пишется строка unit_status_history.
	StatusChanged bool
	// Reason — причина выбытия для EventWithdrawn.
	Reason string
}

// Apply применяет запись АИСТ-М к единице из копии (prev = nil — единицы ещё нет).
// Повтор и устаревшая версия ничего не меняют: версия единицы только растёт.
// Новая единица — UnitRegistered (со статусом на момент регистрации, даже если она уже выбыла);
// переход в выбывший статус — UnitWithdrawn; остальное, включая IN_STOCK ↔ QUARANTINE, — UnitUpdated.
func Apply(prev *Unit, in SourceUnit, newID uuid.UUID, receivedAt time.Time) (Change, error) {
	if err := in.Validate(); err != nil {
		return Change{}, err
	}
	if !IsWithdrawn(in.Status) {
		in.WithdrawalReason = ""
	}
	if prev == nil {
		return Change{
			Event:         EventRegistered,
			Unit:          Unit{UnitID: newID, SourceUnit: in, StatusReceivedAt: receivedAt},
			StatusChanged: true,
		}, nil
	}
	if in.Version <= prev.Version {
		return Change{Event: EventNone, Unit: *prev}, nil
	}
	next := Unit{UnitID: prev.UnitID, SourceUnit: in, StatusReceivedAt: receivedAt}
	ch := Change{Event: EventUpdated, Unit: next, StatusChanged: prev.Status != in.Status}
	if !IsWithdrawn(prev.Status) && IsWithdrawn(in.Status) {
		ch.Event = EventWithdrawn
		ch.Reason = WithdrawReason(in.Status, in.WithdrawalReason)
	}
	return ch, nil
}

// ProtectedVolume — защищённый объём источника по виду компонента в единицах (NFR013). На этапе 1
// задаётся демо-эндпоинтом; позже его считает planning, а публикует inventory как владелец запаса.
type ProtectedVolume struct {
	ClinicID      int64
	ComponentKind string
	VolumeUnits   int64
	Version       int64
	Reason        string
}

// Validate проверяет защищённый объём.
func (p ProtectedVolume) Validate() error {
	switch {
	case p.ClinicID <= 0:
		return fmt.Errorf("%w: не задана клиника", ErrInvalidProtectedVolume)
	case p.ComponentKind == "":
		return fmt.Errorf("%w: не задан вид компонента", ErrInvalidProtectedVolume)
	case p.VolumeUnits < 0:
		return fmt.Errorf("%w: отрицательный объём", ErrInvalidProtectedVolume)
	}
	return nil
}

// Key — ключ сообщения: clinic_id:kind, чтобы версии одного источника и вида шли по порядку.
func (p ProtectedVolume) Key() string {
	return fmt.Sprintf("%d:%s", p.ClinicID, p.ComponentKind)
}
