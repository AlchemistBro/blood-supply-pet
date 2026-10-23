// Package inventory — контракт контекста «Запас» (O01, O02): события о единицах компонентов крови
// в топике inventory.events, ключ — unit_id (для снимков и защищённого объёма — clinic_id:kind).
//
// Исходный код единицы АИСТ-М (aist_unit_code) и номер донации наружу не публикуются: только суррогат
// unit_id (ADR-0010). Версия единицы растёт только вперёд: получатель применяет событие, если его
// version больше сохранённой, иначе это дубль или устаревшее сообщение.
//
// Правило публикации: выбытие из учёта (ISSUED, DISCARDED, TRANSFERRED; истёкшая единица приходит как DISCARDED
// с причиной EXPIRED) публикуется только как UnitWithdrawn. UnitUpdated — только для единиц, которые остаются
// в учёте: IN_STOCK ↔ QUARANTINE, новый срок годности или место хранения (ADR-0027).
package inventory

import (
	"time"

	"github.com/google/uuid"
)

// Статус единицы — коды справочника ref_unit_status (libs/refdata): статусы единицы в базе, в том числе
// «на карантине» (ПП РФ № 641, п. 62 а); закрепляется только IN_STOCK — решение проекта (ADR-0027).
const (
	StatusInStock     = "IN_STOCK"
	StatusQuarantine  = "QUARANTINE"
	StatusIssued      = "ISSUED"
	StatusDiscarded   = "DISCARDED"
	StatusTransferred = "TRANSFERRED"
)

// Statuses — все статусы единицы в порядке справочника.
var Statuses = []string{StatusInStock, StatusQuarantine, StatusIssued, StatusDiscarded, StatusTransferred}

// UpdatedStatuses — статусы, допустимые в UnitUpdated: единица остаётся в учёте. Выбытие — только UnitWithdrawn.
var UpdatedStatuses = []string{StatusInStock, StatusQuarantine}

// WithdrawReasons — все причины выбытия в порядке схемы.
var WithdrawReasons = []string{WithdrawIssued, WithdrawDiscarded, WithdrawTransferred, WithdrawExpired}

// Причины выбытия (UnitWithdrawn). Система знает о выдаче единицы, но не о трансфузии: её фиксирует
// клиника вне системы, поэтому причины TRANSFUSED нет.
const (
	WithdrawIssued      = "ISSUED"
	WithdrawDiscarded   = "DISCARDED"
	WithdrawTransferred = "TRANSFERRED"
	WithdrawExpired     = "EXPIRED"
)

// UnitRegistered — единица появилась в копии запаса по данным АИСТ-М.
type UnitRegistered struct {
	UnitID        uuid.UUID `json:"unit_id"`
	ClinicID      int64     `json:"clinic_id"`
	ComponentKind string    `json:"component_kind"`
	BloodGroup    string    `json:"blood_group"`
	RhPositive    bool      `json:"rh_positive"`
	// Phenotype — квазиидентификатор: потребители не пишут его в логи.
	Phenotype       string    `json:"phenotype,omitempty"`
	VolumeML        int64     `json:"volume_ml"`
	ExpiresAt       time.Time `json:"expires_at"`
	StorageLocation string    `json:"storage_location"`
	// Status — статус на момент регистрации: единица может прийти сразу на карантине.
	Status  string `json:"status"`
	Version int64  `json:"version"`
}

// MessageType реализует contracts.Payload.
func (UnitRegistered) MessageType() string { return "UnitRegistered" }

// SchemaVersion реализует contracts.Payload.
func (UnitRegistered) SchemaVersion() int { return 1 }

// UnitUpdated — у единицы, остающейся в учёте, изменились статус (IN_STOCK ↔ QUARANTINE), место хранения
// или срок годности. Выбытие этим событием не публикуется.
type UnitUpdated struct {
	UnitID          uuid.UUID  `json:"unit_id"`
	ClinicID        int64      `json:"clinic_id"`
	ComponentKind   string     `json:"component_kind"`
	Status          string     `json:"status"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	StorageLocation string     `json:"storage_location,omitempty"`
	Version         int64      `json:"version"`
	ChangedAt       time.Time  `json:"changed_at"`
}

// MessageType реализует contracts.Payload.
func (UnitUpdated) MessageType() string { return "UnitUpdated" }

// SchemaVersion реализует contracts.Payload.
func (UnitUpdated) SchemaVersion() int { return 1 }

// UnitWithdrawn — единица выбыла из наличия: выдана, списана, передана или истёк срок годности.
// Для закреплённой единицы это нарушение обязательства (transfer: → BROKEN).
type UnitWithdrawn struct {
	UnitID        uuid.UUID `json:"unit_id"`
	ClinicID      int64     `json:"clinic_id"`
	ComponentKind string    `json:"component_kind"`
	Reason        string    `json:"reason"`
	Version       int64     `json:"version"`
}

// MessageType реализует contracts.Payload.
func (UnitWithdrawn) MessageType() string { return "UnitWithdrawn" }

// SchemaVersion реализует contracts.Payload.
func (UnitWithdrawn) SchemaVersion() int { return 1 }

// ProtectedVolumePublished — защищённый объём источника по виду компонента в единицах (норматив + фоновая
// потребность + обязательства). Считает planning, публикует inventory как владелец запаса; до появления
// planning — демо-эндпоинт inventory. Хранится с версией и основанием.
type ProtectedVolumePublished struct {
	ClinicID      int64  `json:"clinic_id"`
	ComponentKind string `json:"component_kind"`
	VolumeUnits   int64  `json:"volume_units"`
	Version       int64  `json:"version"`
	Reason        string `json:"reason"`
}

// MessageType реализует contracts.Payload.
func (ProtectedVolumePublished) MessageType() string { return "ProtectedVolumePublished" }

// SchemaVersion реализует contracts.Payload.
func (ProtectedVolumePublished) SchemaVersion() int { return 1 }
