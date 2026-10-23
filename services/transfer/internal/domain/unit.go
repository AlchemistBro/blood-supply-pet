package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// UnitStatus — статус единицы компонента крови по данным inventory (АИСТ-М, C01).
type UnitStatus string

// Статусы единицы из inventory.
const (
	UnitInStock     UnitStatus = "IN_STOCK"
	UnitQuarantine  UnitStatus = "QUARANTINE"
	UnitIssued      UnitStatus = "ISSUED"
	UnitDiscarded   UnitStatus = "DISCARDED"
	UnitTransferred UnitStatus = "TRANSFERRED"
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
// и не закрепляется (ПП РФ № 641, п. 62), выбывшая — тем более.
func (s UnitStatus) Allocatable() bool { return s == UnitInStock }

// CopyStatus — статус единицы в копии transfer. Причины выбытия для подбора не важны, поэтому сведены в WITHDRAWN.
type CopyStatus string

// Статусы копии единицы.
const (
	CopyInStock    CopyStatus = "IN_STOCK"
	CopyQuarantine CopyStatus = "QUARANTINE"
	CopyWithdrawn  CopyStatus = "WITHDRAWN"
)

// CopyStatusOf переводит статус inventory в статус копии.
func CopyStatusOf(s UnitStatus) (CopyStatus, error) {
	switch s {
	case UnitInStock:
		return CopyInStock, nil
	case UnitQuarantine:
		return CopyQuarantine, nil
	case UnitIssued, UnitDiscarded, UnitTransferred:
		return CopyWithdrawn, nil
	default:
		return "", fmt.Errorf("неизвестный статус единицы %q", s)
	}
}

// UnitCopy — единица в локальной копии transfer (проекция inventory.events с версией).
// Фенотип хранится только для проверки спецификации замены и никуда не выводится.
type UnitCopy struct {
	UnitID        uuid.UUID
	ClinicID      int64
	ComponentKind string
	BloodGroup    string
	RhPositive    bool
	Phenotype     *string
	VolumeML      int64
	ExpiresAt     time.Time
	Status        CopyStatus
	Version       int64
}

// BreakReason — почему обязательство перешло в BROKEN.
type BreakReason string

// Причины нарушения.
const (
	BreakUnitWithdrawn   BreakReason = "UNIT_WITHDRAWN"
	BreakUnitQuarantined BreakReason = "UNIT_QUARANTINED"
)

// BreakReasonFor — нарушает ли новый статус закреплённой единицы обязательство и с какой причиной.
// Карантин закреплённой единицы равен выбытию: отправлять её нельзя (ПП РФ № 641, п. 62).
func BreakReasonFor(s CopyStatus) (BreakReason, bool) {
	switch s {
	case CopyQuarantine:
		return BreakUnitQuarantined, true
	case CopyWithdrawn:
		return BreakUnitWithdrawn, true
	case CopyInStock:
		return "", false
	default:
		return "", false
	}
}

// Температурные режимы перевозки для RequestTransport. Демо: режимы по виду компонента, без учёта упаковки.
const (
	RegimeRefrigerated = "REFRIGERATED"  // +2…+6 °C, эритроцитсодержащие
	RegimeFrozen       = "FROZEN"        // не выше −25 °C, плазма и криопреципитат
	RegimeRoomAgitated = "ROOM_AGITATED" // +20…+24 °C с перемешиванием, тромбоциты
)

// TemperatureRegime — режим перевозки по виду компонента. Неизвестный вид — ошибка: режим «по умолчанию»
// для крови опаснее отказа.
func TemperatureRegime(kind string) (string, error) {
	switch kind {
	case "RBC":
		return RegimeRefrigerated, nil
	case "FFP", "CRYO":
		return RegimeFrozen, nil
	case "PLT":
		return RegimeRoomAgitated, nil
	default:
		return "", fmt.Errorf("нет температурного режима для вида %q", kind)
	}
}
