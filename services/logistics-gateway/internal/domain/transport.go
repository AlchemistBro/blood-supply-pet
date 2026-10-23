// Package domain — правила шлюза перевозчика C03 без сети и базы: состояния заказа перевозки, решение после
// сверки с C03 и политика повторов. Перенос TransportGateway прототипа (decide, recordFailure).
//
// Главное правило: вызов C03 с неизвестным исходом не повторяется вслепую. Сначала сверка по нашему clientRef,
// и только если заказа у перевозчика нет — отправка. Перевозчик не дедуплицирует заказы, поэтому «ровно один
// заказ» держит шлюз: первичный ключ по обязательству, аренда строки и сверка (saga-confirmation.md, таблица сбоев).
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Status — состояние заказа перевозки (transport_request.status).
type Status string

// Состояния заказа перевозки.
const (
	// StatusRequesting — команда принята, исход у C03 ещё не известен.
	StatusRequesting Status = "REQUESTING"
	// StatusAccepted — перевозчик принял заказ.
	StatusAccepted Status = "ACCEPTED"
	// StatusRejected — перевозчик отказал.
	StatusRejected Status = "REJECTED"
	// StatusUnknown — C03 долго недоступна; transfer сказали OUTCOME_UNKNOWN, сверка продолжается.
	StatusUnknown Status = "UNKNOWN"
	// StatusFailed — после UNKNOWN сверка заказа не нашла; второй заказ вслепую не шлём.
	StatusFailed Status = "FAILED"
	// StatusCancelled — заказ снят или не создавался (в том числе надгробие отмены).
	StatusCancelled Status = "CANCELLED"
)

// Valid сообщает, известно ли состояние.
func (s Status) Valid() bool {
	switch s {
	case StatusRequesting, StatusAccepted, StatusRejected, StatusUnknown, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// Request — что заказать у перевозчика по обязательству O07. Номеров единиц нет (ADR-0011).
type Request struct {
	ObligationID      uuid.UUID
	RequestID         uuid.UUID
	SourceClinicID    int64
	TargetClinicID    int64
	ComponentKindCode string
	// Quantity — число контейнеров.
	Quantity int
	// TemperatureRegime — режим транспортировки (ПП РФ № 641); значения демо (ADR-0010).
	TemperatureRegime string
}

// ErrInvalidRequest — команда не годится для заказа перевозки.
var ErrInvalidRequest = errors.New("некорректная команда на перевозку")

// Validate проверяет команду до записи. Маршрут сверяет отправитель, здесь только то, без чего заказ не собрать.
func (r Request) Validate() error {
	switch {
	case r.ObligationID == uuid.Nil:
		return fmt.Errorf("%w: нет obligation_id", ErrInvalidRequest)
	case r.RequestID == uuid.Nil:
		return fmt.Errorf("%w: нет request_id", ErrInvalidRequest)
	case r.SourceClinicID <= 0 || r.TargetClinicID <= 0:
		return fmt.Errorf("%w: нет клиники-источника или клиники-получателя", ErrInvalidRequest)
	case r.SourceClinicID == r.TargetClinicID:
		// Перевозка внутри одной клиники не заказывается: такую команду C03 отвергнет при любом повторе.
		return fmt.Errorf("%w: клиника-источник совпадает с клиникой-получателем", ErrInvalidRequest)
	case r.ComponentKindCode == "":
		return fmt.Errorf("%w: нет вида компонента", ErrInvalidRequest)
	case r.Quantity < 1:
		return fmt.Errorf("%w: число контейнеров должно быть положительным", ErrInvalidRequest)
	case r.TemperatureRegime == "":
		return fmt.Errorf("%w: нет температурного режима", ErrInvalidRequest)
	default:
		return nil
	}
}

// Row — заказ перевозки, как его видит шлюз. У надгробия отмены Request пустой, кроме ObligationID.
type Row struct {
	Request
	Status          Status
	ShipmentRef     string
	CancelRequested bool
	Attempts        int
	LastError       string
}

// clientRefPrefix — префикс нашего идентификатора заказа у C03.
const clientRefPrefix = "o07-"

// ClientRef — наш идентификатор заказа у C03: один на обязательство, по нему сверка находит заказ.
func ClientRef(obligationID uuid.UUID) string {
	return clientRefPrefix + strings.ToLower(obligationID.String())
}

// ReplyKind — тип ответа transfer в logistics.replies.
type ReplyKind string

// Ответы шлюза.
const (
	ReplyAccepted  ReplyKind = "TransportAccepted"
	ReplyRejected  ReplyKind = "TransportRejected"
	ReplyCancelled ReplyKind = "TransportCancelled"
)

// Причины TransportRejected.
const (
	// RejectRefused — перевозчик отказал.
	RejectRefused = "REFUSED"
	// RejectOutcomeUnknown — честное «не знаем»: C03 долго недоступна, дошёл ли заказ, проверить не удалось.
	RejectOutcomeUnknown = "OUTCOME_UNKNOWN"
)

// Reply — ответ transfer. Пишется в outbox той же транзакцией, что и исход.
type Reply struct {
	Kind         ReplyKind
	ObligationID uuid.UUID
	ShipmentRef  string
	Reason       string
	Details      string
}

// Outcome — исход шага: новое состояние строки и ответ (nil — ответа нет).
type Outcome struct {
	Status      Status
	ShipmentRef string
	Reply       *Reply
}

// AcceptedOutcome — перевозчик принял заказ shipmentRef.
func AcceptedOutcome(obligationID uuid.UUID, shipmentRef string) Outcome {
	return Outcome{Status: StatusAccepted, ShipmentRef: shipmentRef,
		Reply: &Reply{Kind: ReplyAccepted, ObligationID: obligationID, ShipmentRef: shipmentRef}}
}

// RefusedOutcome — перевозчик отказал с причиной.
func RefusedOutcome(obligationID uuid.UUID, reason string) Outcome {
	return Outcome{Status: StatusRejected,
		Reply: &Reply{Kind: ReplyRejected, ObligationID: obligationID, Reason: RejectRefused, Details: reason}}
}

// CancelledOutcome — заказ снят или не создавался.
func CancelledOutcome(obligationID uuid.UUID) Outcome {
	return Outcome{Status: StatusCancelled, Reply: &Reply{Kind: ReplyCancelled, ObligationID: obligationID}}
}

// CancelledReply — ответ на отмену, когда снимать у C03 нечего (надгробие, отказ, FAILED).
func CancelledReply(obligationID uuid.UUID) Reply {
	return Reply{Kind: ReplyCancelled, ObligationID: obligationID}
}

// NothingToCancel — у C03 нет и не будет заказа по строке в этом состоянии: на отмену отвечаем сразу
// TransportCancelled, не обращаясь к перевозчику.
func NothingToCancel(s Status) bool {
	switch s {
	case StatusRejected, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// Step — что сделать с C03 дальше.
type Step int

// Шаги drive.
const (
	// StepReconcile — спросить C03, есть ли активный заказ с нашим clientRef.
	StepReconcile Step = iota + 1
	// StepCancelAtCarrier — снять заказ у C03; при успехе исход — Decision.Outcome (CANCELLED).
	StepCancelAtCarrier
	// StepSubmit — заказа у C03 нет, отправить.
	StepSubmit
	// StepDone — исход известен без новых вызовов C03.
	StepDone
)

// Decision — следующий шаг и исход, если он уже известен.
type Decision struct {
	Step    Step
	Outcome Outcome
}

// Begin — первый шаг для строки под арендой. ACCEPTED попадает в drive только с флагом отмены.
func (r Row) Begin() Decision {
	if r.Status == StatusAccepted {
		return Decision{Step: StepCancelAtCarrier, Outcome: CancelledOutcome(r.ObligationID)}
	}
	return Decision{Step: StepReconcile}
}

// AfterReconcile решает по итогу сверки. activeShipmentRef — номер активного заказа у C03, пусто — заказа нет.
func (r Row) AfterReconcile(activeShipmentRef string) Decision {
	if activeShipmentRef != "" {
		if r.CancelRequested {
			return Decision{Step: StepCancelAtCarrier, Outcome: CancelledOutcome(r.ObligationID)}
		}
		// Сверка вместо повтора: заказ уже есть (потерянный ответ, падение после ответа C03).
		return Decision{Step: StepDone, Outcome: AcceptedOutcome(r.ObligationID, activeShipmentRef)}
	}
	if r.CancelRequested {
		return Decision{Step: StepDone, Outcome: CancelledOutcome(r.ObligationID)}
	}
	if r.Status == StatusUnknown {
		// transfer уже сказали OUTCOME_UNKNOWN, дежурный мог заказать машину сам: второй заказ не шлём.
		return Decision{Step: StepDone, Outcome: Outcome{Status: StatusFailed}}
	}
	return Decision{Step: StepSubmit}
}

// RetryPolicy — как жить с недоступной C03. Значения по умолчанию из прототипа (application.yaml).
type RetryPolicy struct {
	// MaxAttempts — после стольких неудач подряд REQUESTING становится UNKNOWN.
	MaxAttempts int
	// Backoff — база паузы: Backoff × 2^min(попытка−1, 6).
	Backoff time.Duration
	// UnknownRecheck — как часто перепроверять UNKNOWN.
	UnknownRecheck time.Duration
}

// maxBackoffShift — потолок роста паузы: 2^6 = 64 базы (64 с при базе 1 с).
const maxBackoffShift = 6

// BackoffFor — пауза перед следующей попыткой после неудачной попытки attempt (с 1).
func (p RetryPolicy) BackoffFor(attempt int) time.Duration {
	shift := min(max(attempt-1, 0), maxBackoffShift)
	return p.Backoff * time.Duration(1<<shift)
}

// Failure — что записать, когда C03 не ответила.
type Failure struct {
	Status        Status
	NextAttemptAt time.Time
	// Reply — TransportRejected(OUTCOME_UNKNOWN) при переходе в UNKNOWN, иначе nil.
	Reply *Reply
}

// OnCarrierUnavailable — C03 недоступна или исход вызова неизвестен (обрыв, таймаут). Попытка засчитывается.
// После MaxAttempts строка REQUESTING без отмены становится UNKNOWN, и transfer узнаёт честное «не знаем».
func (p RetryPolicy) OnCarrierUnavailable(r Row, now time.Time) Failure {
	attempt := r.Attempts + 1
	switch {
	case r.Status == StatusRequesting && attempt >= p.MaxAttempts && !r.CancelRequested:
		return Failure{Status: StatusUnknown, NextAttemptAt: now.Add(p.UnknownRecheck), Reply: &Reply{
			Kind: ReplyRejected, ObligationID: r.ObligationID, Reason: RejectOutcomeUnknown,
			Details: fmt.Sprintf("C03 недоступна после %d попыток", attempt),
		}}
	case r.Status == StatusUnknown:
		return Failure{Status: StatusUnknown, NextAttemptAt: now.Add(p.UnknownRecheck)}
	default:
		return Failure{Status: r.Status, NextAttemptAt: now.Add(p.BackoffFor(attempt))}
	}
}
