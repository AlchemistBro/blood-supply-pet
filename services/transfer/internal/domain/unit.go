package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
)

// UnitStatus — статус единицы компонента крови по данным inventory (АИСТ-М, C01). Коды — из контракта
// inventory и справочника ref_unit_status; согласованность проверяет тест домена.
type UnitStatus string

// Статусы единицы из inventory.
const (
	UnitInStock     UnitStatus = inventory.StatusInStock
	UnitQuarantine  UnitStatus = inventory.StatusQuarantine
	UnitIssued      UnitStatus = inventory.StatusIssued
	UnitDiscarded   UnitStatus = inventory.StatusDiscarded
	UnitTransferred UnitStatus = inventory.StatusTransferred
)

// AllUnitStatuses — все статусы единицы.
var AllUnitStatuses = []UnitStatus{UnitInStock, UnitQuarantine, UnitIssued, UnitDiscarded, UnitTransferred}

// ParseUnitStatus разбирает статус единицы из ответа inventory или события.
func ParseUnitStatus(s string) (UnitStatus, error) {
	for _, st := range AllUnitStatuses {
		if string(st) == s {
			return st, nil
		}
	}
	return "", fmt.Errorf("неизвестный статус единицы %q", s)
}

// Allocatable — единицу можно закрепить. Только IN_STOCK: единица на карантине не выдаётся
// и не закрепляется (ПП РФ № 641, п. 62 а), выбывшая — тем более. Совпадает с ref_unit_status.is_available
// (проверяет тест домена).
func (s UnitStatus) Allocatable() bool { return s == UnitInStock }

// WithdrawalStatus — статус единицы по причине UnitWithdrawn. Истёкшая единица списывается: в справочнике
// ref_unit_status отдельного статуса для неё нет, причина EXPIRED сохраняется в unit_copy.status_reason.
func WithdrawalStatus(reason string) (UnitStatus, error) {
	switch reason {
	case inventory.WithdrawIssued:
		return UnitIssued, nil
	case inventory.WithdrawDiscarded, inventory.WithdrawExpired:
		return UnitDiscarded, nil
	case inventory.WithdrawTransferred:
		return UnitTransferred, nil
	default:
		return "", fmt.Errorf("неизвестная причина выбытия %q", reason)
	}
}

// UnitCopy — единица в локальной копии transfer (проекция inventory.events с версией).
// Статус — код справочника ref_unit_status. Фенотип хранится только для проверки спецификации замены
// и никуда не выводится.
type UnitCopy struct {
	SourceStatus  UnitStatus
	UnitID        uuid.UUID
	ClinicID      int64
	ComponentKind string
	BloodGroup    string
	RhPositive    bool
	Phenotype     *string
	VolumeML      int64
	ExpiresAt     time.Time
	Status        UnitStatus
	Version       int64
}

// BreakReason — почему обязательство перешло в BROKEN (obligation.status_reason). Коды совпадают с причинами
// CancelTransport.reason контракта logistics: своего перечисления нет, это одно и то же решение «снять заказ».
type BreakReason string

// Причины нарушения.
const (
	BreakUnitWithdrawn   BreakReason = logistics.CancelUnitWithdrawn
	BreakUnitQuarantined BreakReason = logistics.CancelUnitQuarantined
)

// CancelTransportReason — CancelTransport.reason для нарушения: коды совпадают.
func (r BreakReason) CancelTransportReason() string { return string(r) }

// BrokenEventReason — TransferBroken.reason: при карантине transfer.BrokenQuarantine, при выбытии — причина
// из UnitWithdrawn или код статуса выбытия из UnitUpdated (оба входят в transfer.BrokenReasons).
func BrokenEventReason(r BreakReason, unitReason string) string {
	if r == BreakUnitQuarantined {
		return transfer.BrokenQuarantine
	}
	return unitReason
}

// BreakReasonFor — нарушает ли новый статус закреплённой единицы обязательство и с какой причиной.
// Карантин закреплённой единицы равен выбытию: отправлять её нельзя (ПП РФ № 641, п. 62 а).
func BreakReasonFor(s UnitStatus) (BreakReason, bool) {
	switch s {
	case UnitQuarantine:
		return BreakUnitQuarantined, true
	case UnitIssued, UnitDiscarded, UnitTransferred:
		return BreakUnitWithdrawn, true
	default:
		return "", false
	}
}
