package domain

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
)

// ConfirmationResult — ответ оператору источника в том же запросе: «закреплено, готовьте» или «нет, и вот почему».
// Восемь вариантов по obligation-states.md; набор закрыт неэкспортируемым методом.
type ConfirmationResult interface {
	// Code — стабильный машинный код для API и аудита.
	Code() string
	// Message — текст для оператора на русском.
	Message() string
	confirmationResult()
}

// ConfirmationCodes — коды всех вариантов в порядке obligation-states.md.
var ConfirmationCodes = []string{
	"CONFIRMED", "TASK_WITHDRAWN", "UNITS_UNAVAILABLE", "REJECTED", "UNITS_TAKEN",
	"PROTECTED_VOLUME_EXCEEDED", "INVALID_REQUEST", "SOURCE_SYSTEM_UNAVAILABLE",
}

// Confirmed — единицы закреплены. Shortfall > 0 — частичное подтверждение, на остаток записан TransferShortfallRecorded.
type Confirmed struct {
	ObligationID uuid.UUID
	Units        []uuid.UUID
	Shortfall    int
}

// Partial — подтверждено меньше, чем запрошено.
func (r Confirmed) Partial() bool { return r.Shortfall > 0 }

func (Confirmed) Code() string { return "CONFIRMED" }
func (r Confirmed) Message() string {
	if r.Partial() {
		return fmt.Sprintf("закреплено единиц: %d, недостача %d; готовьте к передаче", len(r.Units), r.Shortfall)
	}
	return fmt.Sprintf("закреплено единиц: %d; готовьте к передаче", len(r.Units))
}
func (Confirmed) confirmationResult() {}

// TaskWithdrawn — задание уже снято (эскалация, отмена, отказ, нарушение): «не отправляйте».
type TaskWithdrawn struct {
	ObligationID uuid.UUID
	Status       Status
}

func (TaskWithdrawn) Code() string { return "TASK_WITHDRAWN" }
func (r TaskWithdrawn) Message() string {
	return fmt.Sprintf("задание снято (статус %s), единицы не отправляйте", r.Status)
}
func (TaskWithdrawn) confirmationResult() {}

// UnitsUnavailable — часть единиц выбыла или на карантине, частичное подтверждение не выбрано. Состояние не менялось.
type UnitsUnavailable struct {
	Units []uuid.UUID
}

func (UnitsUnavailable) Code() string { return "UNITS_UNAVAILABLE" }
func (r UnitsUnavailable) Message() string {
	return fmt.Sprintf("недоступно единиц: %d (выбыли или на карантине); выберите другие или подтвердите частично", len(r.Units))
}
func (UnitsUnavailable) confirmationResult() {}

// RejectReason — причина отказа по заданию.
type RejectReason string

// Причины отказа.
const (
	RejectUnitsUnavailable RejectReason = transfer.RejectUnitsWithdrawn
)

// Rejected — все выбранные единицы недоступны: задание отклонено, подбор предложит следующий вариант.
type Rejected struct {
	ObligationID uuid.UUID
	Reason       RejectReason
}

func (Rejected) Code() string { return "REJECTED" }
func (Rejected) Message() string {
	return "все выбранные единицы выбыли или на карантине; задание отклонено, подбор предложит другой источник"
}
func (Rejected) confirmationResult() {}

// UnitsTaken — единицы уже закреплены за другим обязательством (NFR004). Состояние не менялось.
type UnitsTaken struct {
	Units []uuid.UUID
}

func (UnitsTaken) Code() string { return "UNITS_TAKEN" }
func (r UnitsTaken) Message() string {
	return fmt.Sprintf("единицы уже закреплены за другим заданием: %d; выберите другие", len(r.Units))
}
func (UnitsTaken) confirmationResult() {}

// ProtectedVolumeExceeded — подтверждение опустило бы остаток ниже защищённого объёма (NFR013).
type ProtectedVolumeExceeded struct {
	MaxConfirmable int
}

func (ProtectedVolumeExceeded) Code() string { return "PROTECTED_VOLUME_EXCEEDED" }
func (r ProtectedVolumeExceeded) Message() string {
	return fmt.Sprintf("остаток опустится ниже защищённого объёма; можно подтвердить не больше %d", r.MaxConfirmable)
}
func (ProtectedVolumeExceeded) confirmationResult() {}

// InvalidRequest — запрос не подходит к заданию: чужой источник или вид, лишние или повторяющиеся единицы.
type InvalidRequest struct {
	Reason string
}

func (InvalidRequest) Code() string { return "INVALID_REQUEST" }
func (r InvalidRequest) Message() string {
	return "запрос не подходит к заданию: " + r.Reason
}
func (InvalidRequest) confirmationResult() {}

// SourceSystemUnavailable — АИСТ-М не ответила за таймаут: задание остаётся PENDING, «повторите».
// Общий вариант для подтверждения и замены.
type SourceSystemUnavailable struct{}

func (SourceSystemUnavailable) Code() string { return "SOURCE_SYSTEM_UNAVAILABLE" }
func (SourceSystemUnavailable) Message() string {
	return "АИСТ-М не ответила, статус единиц не проверен; повторите через минуту, вслепую не закрепляем"
}
func (SourceSystemUnavailable) confirmationResult() {}
func (SourceSystemUnavailable) replacementResult()  {}

// Shortfall — недостача при частичном подтверждении.
func Shortfall(requested, confirmed int) int {
	if confirmed >= requested {
		return 0
	}
	return requested - confirmed
}

// ValidateUnitSelection — непустой список разных единиц, не больше requested_qty.
func ValidateUnitSelection(requestedQty int, units []uuid.UUID) *InvalidRequest {
	if len(units) == 0 {
		return &InvalidRequest{Reason: "нужен непустой список единиц"}
	}
	seen := make(map[uuid.UUID]struct{}, len(units))
	for _, u := range units {
		if _, dup := seen[u]; dup {
			return &InvalidRequest{Reason: "единица указана дважды"}
		}
		seen[u] = struct{}{}
	}
	if len(units) > requestedQty {
		return &InvalidRequest{Reason: "единиц больше, чем в задании"}
	}
	return nil
}

// ValidateOwnership — все единицы известны копии, лежат у источника задания и того же вида.
func ValidateOwnership(sourceClinicID int64, kind string, units []uuid.UUID, copies map[uuid.UUID]UnitCopy) *InvalidRequest {
	for _, id := range units {
		u, ok := copies[id]
		if !ok {
			return &InvalidRequest{Reason: "единица неизвестна"}
		}
		if u.ClinicID != sourceClinicID || u.ComponentKind != kind {
			return &InvalidRequest{Reason: "единицы не принадлежат источнику или другого вида"}
		}
	}
	return nil
}

// SplitByStatus делит выбранные единицы по ответу АИСТ-М. Порядок выбора сохраняется.
// Нет в ответе, карантин, выбытие — всё «недоступна».
func SplitByStatus(units []uuid.UUID, statuses map[uuid.UUID]UnitStatus) (present, unavailable []uuid.UUID) {
	for _, id := range units {
		if s, ok := statuses[id]; ok && s.Allocatable() {
			present = append(present, id)
		} else {
			unavailable = append(unavailable, id)
		}
	}
	return present, unavailable
}

// CheckDecision — что делать после проверки статуса в АИСТ-М.
type CheckDecision int

// Решения после проверки.
const (
	// DecideProceed — в транзакцию подтверждения.
	DecideProceed CheckDecision = iota + 1
	// DecideReject — все недоступны: PENDING → REJECTED.
	DecideReject
	// DecideUnitsUnavailable — часть недоступна, частичное не выбрано: ответ без изменений.
	DecideUnitsUnavailable
)

// DecideAfterSourceCheck — шаг 4 подтверждения (obligation-states.md).
func DecideAfterSourceCheck(present, unavailable []uuid.UUID, acceptPartial bool) CheckDecision {
	switch {
	case len(present) == 0:
		return DecideReject
	case len(unavailable) > 0 && !acceptPartial:
		return DecideUnitsUnavailable
	default:
		return DecideProceed
	}
}

// RecheckUnderLock — шаг 5: копия могла узнать о выдаче или карантине, пока шёл вызов АИСТ-М (гонка 2).
// notInStock — единицы из candidates, которых в копии больше нет в наличии.
func RecheckUnderLock(candidates, notInStock []uuid.UUID, acceptPartial bool) ([]uuid.UUID, *UnitsUnavailable) {
	if len(notInStock) == 0 {
		return candidates, nil
	}
	gone := make(map[uuid.UUID]struct{}, len(notInStock))
	for _, id := range notInStock {
		gone[id] = struct{}{}
	}
	var present []uuid.UUID
	for _, id := range candidates {
		if _, ok := gone[id]; !ok {
			present = append(present, id)
		}
	}
	if !acceptPartial || len(present) == 0 {
		return nil, &UnitsUnavailable{Units: notInStock}
	}
	return present, nil
}

// Stock — Остаток источника (клиника × вид), точка сериализации подтверждений (NFR013).
type Stock struct {
	Available int
	Reserved  int
	Protected int
}

// Free — свободно сверх уже закреплённого.
func (s Stock) Free() int { return s.Available - s.Reserved }

// MaxConfirmable — сколько можно подтвердить, не опустившись ниже защищённого объёма.
func (s Stock) MaxConfirmable() int {
	if m := s.Free() - s.Protected; m > 0 {
		return m
	}
	return 0
}

// CheckProtected — NFR013: после подтверждения k единиц free − k ≥ защищённый объём.
func (s Stock) CheckProtected(k int) *ProtectedVolumeExceeded {
	if s.Free()-k < s.Protected {
		return &ProtectedVolumeExceeded{MaxConfirmable: s.MaxConfirmable()}
	}
	return nil
}

// IsConfirmReplay — повтор подтверждения с тем же ключом идемпотентности, пока O07 держит закрепления,
// возвращает тот же Confirmed (obligation-states.md, правило 4). TRANSPORT_REQUESTED тоже: иначе оператор,
// повторивший запрос после ответа перевозчика, услышал бы «задание снято, не отправляйте».
func IsConfirmReplay(status Status, storedKey, key string) bool {
	return key != "" && key == storedKey && status.HoldsAllocations()
}
