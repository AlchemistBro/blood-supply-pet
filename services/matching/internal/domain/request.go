package domain

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrInvalidRequest — заявка не проходит проверку и не подбирается (FR029).
var ErrInvalidRequest = errors.New("некорректная заявка")

// Коды подтверждённых ограничений заявки O08 (request_constraint.constraint_code). Набор закрыт:
// неизвестное ограничение нельзя проверить, а значит нельзя и соблюсти, поэтому такая заявка отклоняется
// при вводе, а не подбирается с молчаливым пропуском ограничения.
const (
	// ConstraintNoGroupSubstitution — только единицы той же группы AB0/Rh, что в заявке; замены группы запрещены.
	ConstraintNoGroupSubstitution = "NO_GROUP_SUBSTITUTION"
	// ConstraintIrradiated — только единицы с признаком IRRADIATED; нет сведений о признаке — единица не подходит.
	ConstraintIrradiated = "IRRADIATED"
	// ConstraintMinRemainingHours — на момент прибытия у единицы остаётся не меньше N часов годности (параметр N).
	ConstraintMinRemainingHours = "MIN_REMAINING_HOURS"
)

// constraintNeedsValue — нужен ли ограничению параметр.
var constraintNeedsValue = map[string]bool{
	ConstraintNoGroupSubstitution: false,
	ConstraintIrradiated:          false,
	ConstraintMinRemainingHours:   true,
}

// unitAttributeConstraints — ограничения, которые требуют признак единицы с тем же кодом.
var unitAttributeConstraints = []string{ConstraintIrradiated}

// Constraint — подтверждённое ограничение заявки O08 в кодированном виде.
type Constraint struct {
	Code  string
	Value string
}

// Request — фактическая клиническая заявка O09 в части, нужной подбору. Номера случая пациента здесь нет:
// подбору он не нужен, заявка идентифицируется RequestID (NFR016).
type Request struct {
	RequestID      uuid.UUID
	ClinicID       int64
	ComponentKind  string
	RequestedUnits int
	Urgency        string
	// BloodGroup — подтверждённая группа AB0/Rh, например "A+".
	BloodGroup string
	// Phenotype — требования фенотипа «есть/нет», например "K-" или "C+ e-"; пусто — требований нет.
	Phenotype   string
	Constraints []Constraint
	// CreatedAt — момент заявки: по нему выбирается действующая версия правил O15.
	CreatedAt time.Time
}

// Validate проверяет заявку до подбора.
func (r Request) Validate() error {
	switch {
	case r.RequestID == uuid.Nil:
		return fmt.Errorf("%w: нет идентификатора", ErrInvalidRequest)
	case r.ClinicID <= 0:
		return fmt.Errorf("%w: не указана клиника", ErrInvalidRequest)
	case r.ComponentKind == "":
		return fmt.Errorf("%w: не указан вид компонента", ErrInvalidRequest)
	case r.RequestedUnits <= 0:
		return fmt.Errorf("%w: количество единиц должно быть положительным", ErrInvalidRequest)
	case !urgencies[r.Urgency]:
		return fmt.Errorf("%w: неизвестная срочность %q", ErrInvalidRequest, r.Urgency)
	case !bloodGroups[r.BloodGroup]:
		return fmt.Errorf("%w: неизвестная группа %q", ErrInvalidRequest, r.BloodGroup)
	case r.CreatedAt.IsZero():
		return fmt.Errorf("%w: не указан момент заявки", ErrInvalidRequest)
	}
	if _, err := ParsePhenotype(r.Phenotype); err != nil {
		// Сами требования фенотипа в текст ошибки не попадают: ошибка может уйти в журнал.
		return fmt.Errorf("%w: требования фенотипа не разобраны", ErrInvalidRequest)
	}
	seen := map[string]bool{}
	for _, c := range r.Constraints {
		needsValue, known := constraintNeedsValue[c.Code]
		if !known {
			return fmt.Errorf("%w: неизвестное ограничение %q", ErrInvalidRequest, c.Code)
		}
		if seen[c.Code] {
			return fmt.Errorf("%w: ограничение %s указано дважды", ErrInvalidRequest, c.Code)
		}
		seen[c.Code] = true
		if !needsValue && c.Value != "" {
			return fmt.Errorf("%w: ограничение %s не принимает параметр", ErrInvalidRequest, c.Code)
		}
		if c.Code == ConstraintMinRemainingHours {
			if _, err := positiveHours(c.Value); err != nil {
				return fmt.Errorf("%w: ограничение %s: %w", ErrInvalidRequest, c.Code, err)
			}
		}
	}
	return nil
}

// HasConstraint — есть ли у заявки ограничение с кодом.
func (r Request) HasConstraint(code string) bool {
	return slices.ContainsFunc(r.Constraints, func(c Constraint) bool { return c.Code == code })
}

// minRemaining — сколько годности должно остаться на момент прибытия (ограничение MIN_REMAINING_HOURS).
func (r Request) minRemaining() time.Duration {
	for _, c := range r.Constraints {
		if c.Code == ConstraintMinRemainingHours {
			h, err := positiveHours(c.Value)
			if err == nil {
				return time.Duration(h) * time.Hour
			}
		}
	}
	return 0
}

func positiveHours(v string) (int, error) {
	h, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || h <= 0 || h > 24*365*5 {
		return 0, fmt.Errorf("параметр %q: нужно целое число часов больше нуля", v)
	}
	return h, nil
}

// Phenotype — набор маркеров «есть/нет»: имя антигена → присутствует.
type Phenotype map[string]bool

// ParsePhenotype разбирает запись вида "C+c-E+e-K-": имя антигена (латиница и цифры) и знак.
// Пробелы, запятые и «;» между маркерами допускаются. Противоречивая запись (K+ и K-) — ошибка.
func ParsePhenotype(s string) (Phenotype, error) {
	p := Phenotype{}
	var name strings.Builder
	for _, ch := range s {
		switch {
		case ch == ' ' || ch == ',' || ch == ';':
			if name.Len() > 0 {
				return nil, errors.New("маркер фенотипа без знака")
			}
		case ch == '+' || ch == '-':
			if name.Len() == 0 {
				return nil, errors.New("знак фенотипа без маркера")
			}
			marker, present := name.String(), ch == '+'
			if prev, ok := p[marker]; ok && prev != present {
				return nil, errors.New("противоречивые требования фенотипа")
			}
			p[marker] = present
			name.Reset()
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
			name.WriteRune(ch)
		default:
			return nil, errors.New("недопустимый символ в записи фенотипа")
		}
	}
	if name.Len() > 0 {
		return nil, errors.New("маркер фенотипа без знака")
	}
	return p, nil
}

// Satisfies — выполняет ли фенотип единицы все требования. Требование к маркеру, о котором у единицы
// нет сведений, не выполнено: ограничение заявки строже нехватки запаса (FR030).
func (p Phenotype) Satisfies(req Phenotype) bool {
	for marker, present := range req {
		got, ok := p[marker]
		if !ok || got != present {
			return false
		}
	}
	return true
}
