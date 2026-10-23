package domain

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// StatusInStock — единственный статус единицы, при котором она предлагается. Карантин, выдача, списание,
// передача и любой неизвестный статус исключают единицу всегда (ПП РФ № 641, п. 62 а).
const StatusInStock = "IN_STOCK"

// Unit — единица компонента крови из проекции unit_projection (копия записи АИСТ-М с версией).
type Unit struct {
	UnitID        uuid.UUID
	ClinicID      int64
	ComponentKind string
	BloodGroup    string
	// Phenotype — фенотип единицы в записи "C+c-E+e-K-"; пусто — сведений нет.
	Phenotype string
	// Attributes — особые признаки единицы (например, IRRADIATED), если источник их передаёт.
	// Нет признака — ограничение, которое его требует, не выполнено.
	Attributes []string
	VolumeML   int64
	ExpiresAt  time.Time
	Status     string
}

// DeliveryEstimate — оценка времени доставки от источника до клиники заявки (проекция
// delivery_estimate_projection, ИНТ-03a). Отсутствие оценки — нормальное состояние: «оценки нет».
type DeliveryEstimate struct {
	Minutes     int
	EstimatedAt time.Time
}

// StockKey — остаток источника по виду компонента.
type StockKey struct {
	ClinicID      int64
	ComponentKind string
}

// SourceStock — доступный запас и защищённый объём источника по виду в единицах (FR025, FR033;
// ADR-0019: защищённый объём по (клиника, вид) публикуется в единицах, ProtectedVolumePublished.volume_units).
type SourceStock struct {
	AvailableUnits int
	ProtectedUnits int
}

// Policy — параметры подбора. Все значения демонстрационные (ADR-0010); нулевое поле заменяется
// значением DemoPolicy.
type Policy struct {
	// ExpiryBucket — шаг сравнения годности при ранжировании: годность внутри одного шага считается
	// равной, и решают следующие факторы (время получения, защищённый запас).
	ExpiryBucket time.Duration
	// UnknownDeliveryMinutes — какое время доставки принять для проверки годности, если оценки нет.
	// В ранжировании такой вариант всё равно стоит после вариантов с оценкой.
	UnknownDeliveryMinutes int
	// EstimateStaleAfter — возраст оценки доставки, после которого она считается устаревшей (ADR-0014).
	// Устаревшая оценка используется (другой нет), но помечается и при равном времени идёт после свежей.
	EstimateStaleAfter time.Duration
	// OfferSubstitutesWhenPreferredCovers — показывать замены, даже когда предпочтительный вариант
	// покрывает заявку полностью. По умолчанию нет: FR030 допускает замены «при отсутствии или
	// недостаточном количестве» предпочтительного варианта.
	OfferSubstitutesWhenPreferredCovers bool
}

// DemoPolicy — демонстрационные параметры подбора.
func DemoPolicy() Policy {
	return Policy{ExpiryBucket: 24 * time.Hour, UnknownDeliveryMinutes: 240, EstimateStaleAfter: 6 * time.Hour}
}

func (p Policy) normalized() Policy {
	d := DemoPolicy()
	if p.EstimateStaleAfter <= 0 {
		p.EstimateStaleAfter = d.EstimateStaleAfter
	}
	if p.ExpiryBucket <= 0 {
		p.ExpiryBucket = d.ExpiryBucket
	}
	if p.UnknownDeliveryMinutes <= 0 {
		p.UnknownDeliveryMinutes = d.UnknownDeliveryMinutes
	}
	return p
}

// Input — всё, от чего зависит решение подбора; одинаковый вход даёт одинаковый результат (FR035, NFR009).
type Input struct {
	Request Request
	// At — момент подбора (из Clock): от него считаются прибытие и годность.
	At    time.Time
	Rules []Rule
	Units []Unit
	// Delivery — оценки времени доставки по клинике-источнику; нет ключа — оценки нет.
	Delivery map[int64]DeliveryEstimate
	// Stock — доступный и защищённый объём источников; нет ключа — сведений нет.
	Stock map[StockKey]SourceStock
	// StockWatermark и StockSyncedAt — версия проекции запаса, по которой построен подбор (водяной знак).
	StockWatermark int64
	StockSyncedAt  time.Time
	Policy         Policy
}

// DeliveryStatus — откуда известно время получения (FR032).
type DeliveryStatus string

const (
	// DeliveryLocal — местный вариант: время получения 0.
	DeliveryLocal DeliveryStatus = "LOCAL"
	// DeliveryEstimated — есть свежая оценка C03.
	DeliveryEstimated DeliveryStatus = "ESTIMATED"
	// DeliveryStale — оценка C03 старше Policy.EstimateStaleAfter или без времени оценки (ADR-0014).
	DeliveryStale DeliveryStatus = "STALE"
	// DeliveryUnknown — оценки нет.
	DeliveryUnknown DeliveryStatus = "UNKNOWN"
)

// Delivery — время получения варианта. Minutes имеет смысл при статусе LOCAL, ESTIMATED и STALE.
type Delivery struct {
	Status      DeliveryStatus
	Minutes     int
	EstimatedAt time.Time
	// Age — возраст оценки на момент подбора; 0, если время оценки неизвестно или оценки нет.
	Age time.Duration
}

// ProtectedStock — расход защищённого объёма источника предлагаемой передачей в единицах (FR033).
// Заполнен, только если Variant.ProtectedKnown.
type ProtectedStock struct {
	AvailableUnits int
	ProtectedUnits int
	// RemainingAfterUnits — доступный запас источника по виду после передачи единиц варианта.
	RemainingAfterUnits int
	// ConsumedUnits — сколько единиц передачи уходит из защищённого объёма.
	ConsumedUnits int
}

// ReplacementSpec — спецификация замены единицы (ADR-0011): по ней transfer проверяет равноценность
// замены без знания клинических правил. Содержит фенотип: не логируется и не уходит в письма.
type ReplacementSpec struct {
	ComponentKind string
	BloodGroup    string
	Phenotype     string
	MinExpiresAt  time.Time
	RuleCode      string
	RuleVersion   int64
}

// Variant — допустимый вариант O10: один источник, один вид, одна группа.
type Variant struct {
	Rank           int
	SourceClinicID int64
	IsLocal        bool
	ComponentKind  string
	BloodGroup     string
	// IsPreferred — точное соответствие заявке по виду и группе.
	IsPreferred bool
	Rule        RuleRef
	// AvailableUnits — сколько пригодных единиц есть у источника по этому варианту.
	AvailableUnits int
	// UnitIDs — конкретные единицы варианта по FEFO, не больше запрошенного; закрепления нет (FR031).
	UnitIDs           []uuid.UUID
	CoversRequest     bool
	EarliestExpiresAt time.Time
	ArrivalAt         time.Time
	Delivery          Delivery
	// ProtectedKnown — есть сведения о запасе и защищённом объёме источника. Для WP3 и UI: false у удалённого
	// варианта — показать «сведений о защищённом объёме нет» (не «не расходует»); у местного варианта всегда
	// false — передачи нет, защищённый объём не применяется.
	ProtectedKnown bool
	Protected      ProtectedStock
	// IsDeficitStockUsed — остаток источника после передачи меньше защищённого объёма (FR033).
	IsDeficitStockUsed bool
	Spec               ReplacementSpec
	Explanation        string

	expiryBucket int64
}

// DeficitReason — причина дефицита заявки (RequestDeficitRecorded.reason).
type DeficitReason string

// DeficitNoAllowedVariant — нет ни одного допустимого варианта.
const DeficitNoAllowedVariant DeficitReason = "NO_ALLOWED_VARIANT"

// Deficit — заявку не покрыть допустимыми вариантами.
type Deficit struct {
	Reason         DeficitReason
	UncoveredUnits int
	Explanation    string
}

// Result — решение подбора с версиями входов (NFR009): варианты по рангу либо дефицит.
type Result struct {
	RequestID uuid.UUID
	MatchedAt time.Time
	// RulesAt — момент, на который взяты версии правил (момент заявки).
	RulesAt        time.Time
	RulesUsed      []RuleRef
	StockWatermark int64
	StockSyncedAt  time.Time
	Variants       []Variant
	Deficit        *Deficit
	// Notes — что исключено и почему, агрегатами без идентификаторов единиц и без фенотипа.
	Notes []string
}

type target struct {
	kind, group string
}

type variantKey struct {
	clinic      int64
	kind, group string
}

// Match подбирает варианты по заявке: шаг 1 — допустимость по правилам O15 и ограничениям O08, шаг 2 —
// ранжирование (FR030, FR031, FR034). Подбор только предлагает: клиническое решение принимает врач.
func Match(in Input) (Result, error) {
	req := in.Request
	if err := req.Validate(); err != nil {
		return Result{}, err
	}
	if in.At.IsZero() {
		return Result{}, errors.New("подбор: не задан момент подбора")
	}
	policy := in.Policy.normalized()
	active, err := ActiveRules(in.Rules, req.CreatedAt)
	if err != nil {
		return Result{}, err
	}
	res := Result{
		RequestID:      req.RequestID,
		MatchedAt:      in.At,
		RulesAt:        req.CreatedAt,
		StockWatermark: in.StockWatermark,
		StockSyncedAt:  in.StockSyncedAt,
	}
	for _, r := range active {
		res.RulesUsed = append(res.RulesUsed, r.Ref())
	}

	permitted, notes := permittedTargets(req, active)
	res.Notes = append(res.Notes, notes...)

	eligible, notes := eligibleUnits(in, policy, permitted)
	res.Notes = append(res.Notes, notes...)

	variants := buildVariants(in, policy, permitted, eligible)
	if !policy.OfferSubstitutesWhenPreferredCovers && slices.ContainsFunc(variants, func(v Variant) bool {
		return v.IsPreferred && v.CoversRequest
	}) {
		before := len(variants)
		variants = slices.DeleteFunc(variants, func(v Variant) bool { return !v.IsPreferred })
		if before != len(variants) {
			res.Notes = append(res.Notes, "предпочтительный вариант покрывает заявку, замены не предлагаются (FR030)")
		}
	}

	slices.SortFunc(variants, compareVariants)
	for i := range variants {
		variants[i].Rank = i + 1
	}
	for i := range variants {
		var prev *Variant
		if i > 0 {
			prev = &variants[i-1]
		}
		variants[i].Explanation = explainVariant(res, req, policy, variants[i], prev, len(variants))
	}
	res.Variants = variants

	if len(variants) == 0 {
		res.Deficit = &Deficit{Reason: DeficitNoAllowedVariant, UncoveredUnits: req.RequestedUnits}
		res.Deficit.Explanation = explainDeficit(res, req)
	}
	return res, nil
}

// permittedTargets — шаг 1 по правилам: какие (вид, группа) разрешены и каким правилом. Ограничения заявки
// применяются здесь же и строго: нехватка запаса не возвращает запрещённую замену.
func permittedTargets(req Request, active []Rule) (map[target]Rule, []string) {
	permitted := map[target]Rule{}
	for _, r := range active {
		if r.IsException || !ruleApplies(r, req) {
			continue
		}
		t := ruleTarget(r, req)
		if cur, ok := permitted[t]; !ok || betterRule(r, cur) {
			permitted[t] = r
		}
	}
	var notes []string
	if len(permitted) == 0 {
		notes = append(notes, fmt.Sprintf("нет действующего правила подбора для вида %s и группы %s", req.ComponentKind, req.BloodGroup))
	}

	// FR030, O08: NO_GROUP_SUBSTITUTION снимает все замены группы, сколько бы их ни разрешали правила.
	if req.HasConstraint(ConstraintNoGroupSubstitution) {
		var removed []string
		for t := range permitted {
			if t.group != req.BloodGroup {
				removed = append(removed, t.kind+" "+t.group)
				delete(permitted, t)
			}
		}
		if len(removed) > 0 {
			slices.Sort(removed)
			notes = append(notes, fmt.Sprintf("ограничение %s исключило замены группы: %s",
				ConstraintNoGroupSubstitution, strings.Join(removed, ", ")))
		}
	}

	// Исключение запрещает вариант (ПР6, matching_rule.is_exception) — любой, включая точное соответствие заявке.
	for _, r := range active {
		if !r.IsException || !ruleApplies(r, req) {
			continue
		}
		t := ruleTarget(r, req)
		if _, ok := permitted[t]; ok {
			delete(permitted, t)
			notes = append(notes, fmt.Sprintf("исключение %s v%d запретило вариант %s %s", r.Code, r.Version, t.kind, t.group))
		}
	}
	return permitted, notes
}

func ruleApplies(r Rule, req Request) bool {
	return r.ComponentKind == req.ComponentKind &&
		(r.RequestedGroup == "" || r.RequestedGroup == req.BloodGroup) &&
		ruleCondition(r).Holds(req)
}

func ruleTarget(r Rule, req Request) target {
	t := target{kind: r.AllowedKind, group: r.AllowedGroup}
	if t.kind == "" {
		t.kind = req.ComponentKind
	}
	if t.group == "" {
		t.group = req.BloodGroup
	}
	return t
}

// betterRule — из нескольких правил, разрешающих одну замену, берётся высший приоритет, затем код.
func betterRule(a, b Rule) bool {
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	return a.Code < b.Code
}

// arrival — когда единицы источника окажутся у клиники заявки.
func arrival(in Input, policy Policy, clinic int64) (time.Time, Delivery) {
	if clinic == in.Request.ClinicID {
		return in.At, Delivery{Status: DeliveryLocal}
	}
	if e, ok := in.Delivery[clinic]; ok && e.Minutes >= 0 {
		d := Delivery{Status: DeliveryEstimated, Minutes: e.Minutes, EstimatedAt: e.EstimatedAt}
		if e.EstimatedAt.IsZero() {
			d.Status = DeliveryStale
		} else {
			d.Age = max(in.At.Sub(e.EstimatedAt), 0)
			if d.Age > policy.EstimateStaleAfter {
				d.Status = DeliveryStale
			}
		}
		return in.At.Add(time.Duration(e.Minutes) * time.Minute), d
	}
	return in.At.Add(time.Duration(policy.UnknownDeliveryMinutes) * time.Minute), Delivery{Status: DeliveryUnknown}
}

// eligibleUnits — единицы, проходящие все строгие проверки, по вариантам.
func eligibleUnits(in Input, policy Policy, permitted map[target]Rule) (map[variantKey][]Unit, []string) {
	req := in.Request
	reqPhenotype, _ := ParsePhenotype(req.Phenotype) // разобран в Validate
	minRemaining := req.minRemaining()
	var attrs []string
	for _, code := range unitAttributeConstraints {
		if req.HasConstraint(code) {
			attrs = append(attrs, code)
		}
	}

	var notInStock, expiring, phenotype int
	missingAttr := map[string]int{}
	out := map[variantKey][]Unit{}
	for _, u := range in.Units {
		if _, ok := permitted[target{kind: u.ComponentKind, group: u.BloodGroup}]; !ok {
			continue
		}
		if u.Status != StatusInStock {
			notInStock++
			continue
		}
		arrivalAt, _ := arrival(in, policy, u.ClinicID)
		if !u.ExpiresAt.After(arrivalAt.Add(minRemaining)) {
			expiring++
			continue
		}
		if len(reqPhenotype) > 0 {
			p, err := ParsePhenotype(u.Phenotype)
			if err != nil || !p.Satisfies(reqPhenotype) {
				phenotype++
				continue
			}
		}
		if code, ok := missingAttribute(u, attrs); ok {
			missingAttr[code]++
			continue
		}
		k := variantKey{clinic: u.ClinicID, kind: u.ComponentKind, group: u.BloodGroup}
		out[k] = append(out[k], u)
	}

	var notes []string
	if notInStock > 0 {
		notes = append(notes, fmt.Sprintf("не в наличии (карантин, выдача, списание, передача): %d ед.", notInStock))
	}
	if expiring > 0 {
		notes = append(notes, fmt.Sprintf("срок годности истекает до прибытия или раньше требуемого запаса годности: %d ед.", expiring))
	}
	if phenotype > 0 {
		notes = append(notes, fmt.Sprintf("не выполняют требования фенотипа заявки или нет сведений о фенотипе: %d ед.", phenotype))
	}
	for _, code := range attrs {
		if n := missingAttr[code]; n > 0 {
			notes = append(notes, fmt.Sprintf("нет признака %s: %d ед.", code, n))
		}
	}
	return out, notes
}

func missingAttribute(u Unit, required []string) (string, bool) {
	for _, code := range required {
		if !slices.Contains(u.Attributes, code) {
			return code, true
		}
	}
	return "", false
}

func buildVariants(in Input, policy Policy, permitted map[target]Rule, eligible map[variantKey][]Unit) []Variant {
	req := in.Request
	minRemaining := req.minRemaining()
	variants := make([]Variant, 0, len(eligible))
	for k, units := range eligible {
		// FEFO: раньше истекающие единицы первыми; при равной годности — по идентификатору.
		slices.SortFunc(units, func(a, b Unit) int {
			if c := a.ExpiresAt.Compare(b.ExpiresAt); c != 0 {
				return c
			}
			return strings.Compare(a.UnitID.String(), b.UnitID.String())
		})
		n := min(req.RequestedUnits, len(units))
		selected := units[:n]
		ids := make([]uuid.UUID, 0, n)
		for _, u := range selected {
			ids = append(ids, u.UnitID)
		}
		rule := permitted[target{kind: k.kind, group: k.group}]
		arrivalAt, delivery := arrival(in, policy, k.clinic)
		v := Variant{
			SourceClinicID:    k.clinic,
			IsLocal:           k.clinic == req.ClinicID,
			ComponentKind:     k.kind,
			BloodGroup:        k.group,
			IsPreferred:       k.kind == req.ComponentKind && k.group == req.BloodGroup,
			Rule:              rule.Ref(),
			AvailableUnits:    len(units),
			UnitIDs:           ids,
			CoversRequest:     len(units) >= req.RequestedUnits,
			EarliestExpiresAt: selected[0].ExpiresAt,
			ArrivalAt:         arrivalAt,
			Delivery:          delivery,
			Spec: ReplacementSpec{
				ComponentKind: k.kind, BloodGroup: k.group, Phenotype: req.Phenotype,
				MinExpiresAt: arrivalAt.Add(minRemaining), RuleCode: rule.Code, RuleVersion: rule.Version,
			},
			expiryBucket: int64(selected[0].ExpiresAt.Sub(in.At) / policy.ExpiryBucket),
		}
		// Защищённый объём охраняет источник при передаче (FR025); местный вариант — не передача.
		if !v.IsLocal {
			if s, ok := in.Stock[StockKey{ClinicID: k.clinic, ComponentKind: k.kind}]; ok {
				// Проекция запаса может отставать от проекции единиц: остаток не уходит ниже нуля.
				remaining := max(s.AvailableUnits-n, 0)
				v.ProtectedKnown = true
				v.Protected = ProtectedStock{AvailableUnits: s.AvailableUnits, ProtectedUnits: s.ProtectedUnits,
					RemainingAfterUnits: remaining, ConsumedUnits: min(max(s.ProtectedUnits-remaining, 0), n)}
				v.IsDeficitStockUsed = remaining < s.ProtectedUnits
			}
		}
		variants = append(variants, v)
	}
	return variants
}

// rankFactor — один фактор ранжирования: имя для объяснения, сравнение и значение текстом.
type rankFactor struct {
	name    string
	compare func(a, b Variant) int
	show    func(v Variant) string
}

// rankFactors — порядок ранжирования FR034. Последние факторы только делают порядок полным.
var rankFactors = []rankFactor{
	{
		name:    "предпочтительный вариант",
		compare: func(a, b Variant) int { return -cmpBool(a.IsPreferred, b.IsPreferred) },
		show:    func(v Variant) string { return yesNo(v.IsPreferred) },
	},
	{
		name:    "приоритет правила",
		compare: func(a, b Variant) int { return cmp.Compare(a.Rule.Priority, b.Rule.Priority) },
		show:    func(v Variant) string { return fmt.Sprintf("%d", v.Rule.Priority) },
	},
	{
		name:    "годность на момент прибытия (FEFO)",
		compare: func(a, b Variant) int { return cmp.Compare(a.expiryBucket, b.expiryBucket) },
		show:    func(v Variant) string { return "до " + formatTime(v.EarliestExpiresAt) },
	},
	{
		// Оценки нет — последним. Устаревшая оценка сравнивается по минутам наравне со свежей: это всё равно
		// лучшее, что известно о расстоянии, и уводить её ниже любой свежей оценки было бы грубее, чем
		// показать возраст; при равном времени свежая оценка выше.
		name: "время получения",
		compare: func(a, b Variant) int {
			return cmp.Or(
				cmpBool(a.Delivery.Status == DeliveryUnknown, b.Delivery.Status == DeliveryUnknown),
				cmp.Compare(a.Delivery.Minutes, b.Delivery.Minutes),
				cmpBool(a.Delivery.Status == DeliveryStale, b.Delivery.Status == DeliveryStale),
			)
		},
		show: func(v Variant) string {
			switch v.Delivery.Status {
			case DeliveryUnknown:
				return "оценки нет"
			case DeliveryStale:
				return fmt.Sprintf("%d мин, оценка устарела", v.Delivery.Minutes)
			default:
				return fmt.Sprintf("%d мин", v.Delivery.Minutes)
			}
		},
	},
	{
		name: "расход защищённого запаса источника",
		compare: func(a, b Variant) int {
			if c := cmpBool(!a.IsLocal && !a.ProtectedKnown, !b.IsLocal && !b.ProtectedKnown); c != 0 {
				return c
			}
			return cmp.Compare(a.Protected.ConsumedUnits, b.Protected.ConsumedUnits)
		},
		show: func(v Variant) string {
			if !v.IsLocal && !v.ProtectedKnown {
				return "сведений нет"
			}
			return fmt.Sprintf("%d ед.", v.Protected.ConsumedUnits)
		},
	},
	{
		name:    "номер клиники-источника",
		compare: func(a, b Variant) int { return cmp.Compare(a.SourceClinicID, b.SourceClinicID) },
		show:    func(v Variant) string { return fmt.Sprintf("%d", v.SourceClinicID) },
	},
	{
		name: "вид и группа",
		compare: func(a, b Variant) int {
			return cmp.Or(strings.Compare(a.ComponentKind, b.ComponentKind), strings.Compare(a.BloodGroup, b.BloodGroup))
		},
		show: func(v Variant) string { return v.ComponentKind + " " + v.BloodGroup },
	},
}

func compareVariants(a, b Variant) int {
	for _, f := range rankFactors {
		if c := f.compare(a, b); c != 0 {
			return c
		}
	}
	return 0
}

// decidingFactor — первый фактор, который ставит prev выше v.
func decidingFactor(prev, v Variant) (rankFactor, bool) {
	for _, f := range rankFactors {
		if f.compare(prev, v) != 0 {
			return f, true
		}
	}
	return rankFactor{}, false
}

// cmpBool: false < true.
func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	default:
		return 1
	}
}
