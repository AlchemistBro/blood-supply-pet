package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ReplacementResult — ответ на замену единицы равноценной до отправки (ADR-0011). Шесть вариантов.
type ReplacementResult interface {
	Code() string
	Message() string
	replacementResult()
}

// ReplacementCodes — коды всех вариантов замены.
var ReplacementCodes = []string{
	"REPLACED", "NOT_ALLOWED", "SPEC_MISMATCH", "UNIT_UNAVAILABLE", "UNIT_TAKEN", "SOURCE_SYSTEM_UNAVAILABLE",
}

// Replaced — старое закрепление снято, новое поставлено; резерв и заказ перевозчику не менялись.
type Replaced struct {
	ObligationID uuid.UUID
	OldUnitID    uuid.UUID
	NewUnitID    uuid.UUID
}

func (Replaced) Code() string       { return "REPLACED" }
func (Replaced) Message() string    { return "единица заменена равноценной" }
func (Replaced) replacementResult() {}

// NotAllowed — замена недопустима в этом статусе (до подтверждения, после отправки, в конечном).
type NotAllowed struct {
	Status Status
}

func (NotAllowed) Code() string { return "NOT_ALLOWED" }
func (r NotAllowed) Message() string {
	return fmt.Sprintf("замена недопустима в статусе %s", r.Status)
}
func (NotAllowed) replacementResult() {}

// SpecMismatch — кандидат не подходит под спецификацию из выбора врача или не закреплённая единица заменяется.
type SpecMismatch struct {
	Reason MismatchReason
}

func (SpecMismatch) Code() string { return "SPEC_MISMATCH" }
func (r SpecMismatch) Message() string {
	return "единица не равноценна: " + r.Reason.Text()
}
func (SpecMismatch) replacementResult() {}

// UnitUnavailable — кандидат выбыл или на карантине.
type UnitUnavailable struct {
	UnitID uuid.UUID
}

func (UnitUnavailable) Code() string { return "UNIT_UNAVAILABLE" }
func (UnitUnavailable) Message() string {
	return "единица выбыла или на карантине"
}
func (UnitUnavailable) replacementResult() {}

// UnitTaken — кандидат закреплён за другим обязательством (NFR004).
type UnitTaken struct {
	UnitID uuid.UUID
}

func (UnitTaken) Code() string { return "UNIT_TAKEN" }
func (UnitTaken) Message() string {
	return "единица уже закреплена за другим заданием"
}
func (UnitTaken) replacementResult() {}

// MismatchReason — почему кандидат не равноценен. Тексты без значений спецификации: фенотип —
// квазиидентификатор и не попадает ни в ответы, ни в логи (ADR-0011).
type MismatchReason string

// Причины несоответствия.
const (
	MismatchNotAllocated MismatchReason = "NOT_ALLOCATED"
	MismatchSameUnit     MismatchReason = "SAME_UNIT"
	MismatchUnknownUnit  MismatchReason = "UNKNOWN_UNIT"
	MismatchSource       MismatchReason = "SOURCE"
	MismatchKind         MismatchReason = "KIND"
	MismatchBloodGroup   MismatchReason = "BLOOD_GROUP"
	MismatchRh           MismatchReason = "RH"
	MismatchPhenotype    MismatchReason = "PHENOTYPE"
	MismatchExpiry       MismatchReason = "EXPIRY"
)

// Text — причина для оператора.
func (r MismatchReason) Text() string {
	switch r {
	case MismatchNotAllocated:
		return "заменяемая единица не закреплена за заданием"
	case MismatchSameUnit:
		return "новая единица совпадает с заменяемой"
	case MismatchUnknownUnit:
		return "единица неизвестна"
	case MismatchSource:
		return "единица другого источника"
	case MismatchKind:
		return "другой вид компонента"
	case MismatchBloodGroup:
		return "группа не совпадает"
	case MismatchRh:
		return "резус не совпадает"
	case MismatchPhenotype:
		return "фенотип не совпадает"
	case MismatchExpiry:
		return "годность на момент прибытия недостаточна"
	default:
		return "не соответствует спецификации"
	}
}

// ReplacementSpec — спецификация допустимой замены. Её считает matching (или planning) по правилу O15 при выборе,
// transfer только проверяет кандидата (ADR-0011). Nil-поле — «любое значение».
type ReplacementSpec struct {
	BloodGroup   *string
	RhPositive   *bool
	Phenotype    *string
	MinExpiresAt *time.Time
	RuleVersion  string
}

// Mismatch проверяет кандидата: источник, вид, группа AB0, резус, фенотип, годность на момент прибытия.
// Пустая строка — кандидат равноценен.
func (s ReplacementSpec) Mismatch(sourceClinicID int64, kind string, u UnitCopy) MismatchReason {
	switch {
	case u.ClinicID != sourceClinicID:
		return MismatchSource
	case u.ComponentKind != kind:
		return MismatchKind
	case s.BloodGroup != nil && abo(*s.BloodGroup) != abo(u.BloodGroup):
		return MismatchBloodGroup
	}
	if rh, ok := s.requiredRh(); ok && rh != u.RhPositive {
		return MismatchRh
	}
	if s.Phenotype != nil && (u.Phenotype == nil || *u.Phenotype != *s.Phenotype) {
		return MismatchPhenotype
	}
	if s.MinExpiresAt != nil && u.ExpiresAt.Before(*s.MinExpiresAt) {
		return MismatchExpiry
	}
	return ""
}

// requiredRh — требование к резусу: отдельное поле или знак в коде группы («A+»).
func (s ReplacementSpec) requiredRh() (bool, bool) {
	if s.RhPositive != nil {
		return *s.RhPositive, true
	}
	if s.BloodGroup != nil {
		switch {
		case strings.HasSuffix(*s.BloodGroup, "+"):
			return true, true
		case strings.HasSuffix(*s.BloodGroup, "-"):
			return false, true
		}
	}
	return false, false
}

// abo — группа AB0 без знака резуса: контракты передают группу то кодом справочника («A+»), то отдельно от резуса («A»).
func abo(group string) string {
	return strings.ToUpper(strings.TrimRight(strings.TrimSpace(group), "+-"))
}
