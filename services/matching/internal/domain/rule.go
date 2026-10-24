package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrInvalidRule — версия правила O15 нарушает структуру справочника (FR047).
var ErrInvalidRule = errors.New("некорректное правило подбора")

// bloodGroups — коды AB0/Rh (FR004, FR005); перечень закрыт, совпадает с ref_blood_group
// (libs/refdata/data/dicts.json, сверяет тест). Домен не импортирует libs/refdata: там драйвер БД.
var bloodGroups = map[string]bool{
	"O+": true, "O-": true, "A+": true, "A-": true, "B+": true, "B-": true, "AB+": true, "AB-": true,
}

// urgencies — коды срочности; перечень закрыт, совпадает с ref_urgency (сверяет тест).
var urgencies = map[string]bool{"PLANNED": true, "URGENT": true, "EMERGENCY": true}

// BloodGroupCodes — известные домену коды групп по алфавиту.
func BloodGroupCodes() []string { return sortedKeys(bloodGroups) }

// UrgencyCodes — известные домену коды срочности по алфавиту.
func UrgencyCodes() []string { return sortedKeys(urgencies) }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Rule — версия правила подбора O15 (matching_rule). Содержание правил синтетическое (ADR-0010):
// правило не устанавливает совместимость, а описывает, какую замену разрешает демо-набор.
type Rule struct {
	RuleID        uuid.UUID
	Code          string
	Version       int64
	ComponentKind string
	// RequestedGroup — группа в заявке, к которой применяется правило; пусто — любая.
	RequestedGroup string
	// AllowedGroup — группа единицы, которую разрешает правило; пусто — та же, что в заявке.
	AllowedGroup string
	// AllowedKind — вид единицы при замене вида; пусто — тот же вид, что в заявке.
	AllowedKind string
	// Condition — application_condition, синтаксис в ParseCondition.
	Condition string
	// IsException — правило запрещает замену, которую разрешили бы другие правила.
	IsException bool
	// Priority — 1 — высший приоритет при ранжировании.
	Priority      int
	BasisDocument string
	BasisClause   string
	// ValidFrom — начало действия, включительно.
	ValidFrom time.Time
	// ValidTo — конец действия, не включительно; нулевое значение — без срока.
	ValidTo time.Time
}

// RuleRef — ссылка на версию правила в варианте и объяснении (FR030, NFR009).
type RuleRef struct {
	RuleID        uuid.UUID
	Code          string
	Version       int64
	Priority      int
	IsException   bool
	BasisDocument string
	BasisClause   string
}

// Ref возвращает ссылку на версию правила.
func (r Rule) Ref() RuleRef {
	return RuleRef{RuleID: r.RuleID, Code: r.Code, Version: r.Version, Priority: r.Priority,
		IsException: r.IsException, BasisDocument: r.BasisDocument, BasisClause: r.BasisClause}
}

// Validate проверяет структуру версии правила: условие, результат, основание, версия, срок действия.
func (r Rule) Validate() error {
	switch {
	case r.Code == "":
		return fmt.Errorf("%w: пустой код", ErrInvalidRule)
	case r.Version <= 0:
		return fmt.Errorf("%w: %s: версия должна быть положительной", ErrInvalidRule, r.Code)
	case r.ComponentKind == "":
		return fmt.Errorf("%w: %s v%d: не указан вид компонента", ErrInvalidRule, r.Code, r.Version)
	case r.Priority <= 0:
		return fmt.Errorf("%w: %s v%d: приоритет должен быть положительным", ErrInvalidRule, r.Code, r.Version)
	case r.BasisDocument == "" || r.BasisClause == "":
		return fmt.Errorf("%w: %s v%d: не указано основание (документ и пункт)", ErrInvalidRule, r.Code, r.Version)
	case r.ValidFrom.IsZero():
		return fmt.Errorf("%w: %s v%d: не указано начало действия", ErrInvalidRule, r.Code, r.Version)
	case !r.ValidTo.IsZero() && !r.ValidTo.After(r.ValidFrom):
		return fmt.Errorf("%w: %s v%d: конец действия не позже начала", ErrInvalidRule, r.Code, r.Version)
	case r.RequestedGroup != "" && !bloodGroups[r.RequestedGroup]:
		return fmt.Errorf("%w: %s v%d: неизвестная группа заявки %q", ErrInvalidRule, r.Code, r.Version, r.RequestedGroup)
	case r.AllowedGroup != "" && !bloodGroups[r.AllowedGroup]:
		return fmt.Errorf("%w: %s v%d: неизвестная группа замены %q", ErrInvalidRule, r.Code, r.Version, r.AllowedGroup)
	}
	if _, err := ParseCondition(r.Condition); err != nil {
		return fmt.Errorf("%w: %s v%d: %w", ErrInvalidRule, r.Code, r.Version, err)
	}
	return nil
}

// ActiveAt — действует ли версия в момент at.
func (r Rule) ActiveAt(at time.Time) bool {
	return !at.Before(r.ValidFrom) && (r.ValidTo.IsZero() || at.Before(r.ValidTo))
}

// ActiveRules проверяет справочник и возвращает по одной версии каждого правила, действующей в момент at,
// упорядоченные по коду. Перекрытие версий — ошибка ввода; чтобы решение оставалось воспроизводимым,
// берётся старшая версия (ввод версии в эксплуатацию закрывает предыдущую, это проверяет приложение).
func ActiveRules(rules []Rule, at time.Time) ([]Rule, error) {
	byCode := map[string]Rule{}
	for _, r := range rules {
		if err := r.Validate(); err != nil {
			return nil, err
		}
		if !r.ActiveAt(at) {
			continue
		}
		if cur, ok := byCode[r.Code]; !ok || r.Version > cur.Version {
			byCode[r.Code] = r
		}
	}
	out := make([]Rule, 0, len(byCode))
	for _, r := range byCode {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Rule) int { return strings.Compare(a.Code, b.Code) })
	return out, nil
}

// Ключи кодированного условия применения правила.
const (
	condUrgency       = "URGENCY"
	condConstraint    = "CONSTRAINT"
	condNotConstraint = "NOT_CONSTRAINT"
	condAlways        = "ALWAYS"
)

type clause struct {
	key    string
	values []string
}

// Condition — разобранное условие применения правила; все части должны выполняться.
type Condition struct {
	clauses []clause
}

// ParseCondition разбирает application_condition:
//
//	""  или  ALWAYS                    — всегда
//	URGENCY=URGENT|EMERGENCY           — срочность заявки из перечня
//	CONSTRAINT=IRRADIATED              — у заявки есть одно из ограничений
//	NOT_CONSTRAINT=NO_GROUP_SUBSTITUTION — у заявки нет ни одного из ограничений
//
// Части соединяются «;» и должны выполняться все.
func ParseCondition(s string) (Condition, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == condAlways {
		return Condition{}, nil
	}
	var c Condition
	for part := range strings.SplitSeq(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, raw, ok := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		if !ok {
			return Condition{}, fmt.Errorf("условие %q: ожидали KEY=VALUE", part)
		}
		switch key {
		case condUrgency, condConstraint, condNotConstraint:
		default:
			return Condition{}, fmt.Errorf("условие %q: неизвестный ключ %q", part, key)
		}
		var values []string
		for v := range strings.SplitSeq(raw, "|") {
			v = strings.TrimSpace(v)
			if v == "" {
				return Condition{}, fmt.Errorf("условие %q: пустое значение", part)
			}
			// Значения только из закрытых наборов: опечатка в условии иначе молча выключила бы правило.
			if key == condUrgency && !urgencies[v] {
				return Condition{}, fmt.Errorf("условие %q: неизвестная срочность %q", part, v)
			}
			if _, known := constraintNeedsValue[v]; key != condUrgency && !known {
				return Condition{}, fmt.Errorf("условие %q: неизвестное ограничение %q", part, v)
			}
			values = append(values, v)
		}
		c.clauses = append(c.clauses, clause{key: key, values: values})
	}
	return c, nil
}

// Holds — выполняется ли условие для заявки.
func (c Condition) Holds(req Request) bool {
	for _, cl := range c.clauses {
		var ok bool
		switch cl.key {
		case condUrgency:
			ok = slices.Contains(cl.values, req.Urgency)
		case condConstraint:
			ok = slices.ContainsFunc(cl.values, req.HasConstraint)
		case condNotConstraint:
			ok = !slices.ContainsFunc(cl.values, req.HasConstraint)
		}
		if !ok {
			return false
		}
	}
	return true
}

// ruleCondition — условие уже проверенного правила; ошибка разбора исключена Validate.
func ruleCondition(r Rule) Condition {
	c, err := ParseCondition(r.Condition)
	if err != nil {
		return Condition{clauses: []clause{{key: condUrgency}}} // никогда не выполняется
	}
	return c
}
