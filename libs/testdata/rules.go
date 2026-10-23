package testdata

import "time"

// DemoRulesBasisDocument — основание каждого демо-правила O15. Настоящих утверждённых заказчиком правил
// подбора нет (ADR-0010): структура правила настоящая, содержание — нет.
const DemoRulesBasisDocument = "ДЕМО: синтетические правила, не клинические"

// MatchingRule — версия правила подбора O15 в терминах словаря ПР6 (requests.MatchingRule).
// ДЕМО: синтетические правила, не клинические. Правила не утверждают совместимость: они показывают
// механизм «условие → разрешённая замена → исключение → приоритет → основание → версия» (FR047).
type MatchingRule struct {
	RuleCode          string
	VersionNo         int64
	ComponentKindCode string
	// RequestedGroupCode — группа в заявке; пусто — любая.
	RequestedGroupCode string
	// AllowedGroupCode — группа единицы, которую разрешает правило; пусто — та же, что в заявке.
	AllowedGroupCode string
	// AllowedKindCode — вид единицы при замене вида; пусто — тот же вид.
	AllowedKindCode string
	// ApplicationCondition — условие в кодированном виде: пусто или ALWAYS — всегда;
	// иначе условия через «;», каждое KEY=V1|V2, ключи URGENCY, CONSTRAINT, NOT_CONSTRAINT.
	ApplicationCondition string
	// IsException — правило-исключение: запрещает замену, которую разрешили бы другие правила.
	IsException bool
	// RulePriority — 1 — самый высокий приоритет при ранжировании.
	RulePriority  int
	BasisDocument string
	BasisClause   string
	// ValidFrom — начало действия версии (включительно).
	ValidFrom time.Time
	// ValidTo — конец действия (не включительно); нулевое значение — действует без срока.
	ValidTo time.Time
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// demoRules — десять версий девяти демо-правил. ДЕМО: синтетические правила, не клинические.
var demoRules = []MatchingRule{
	{
		RuleCode: "DEMO_RBC_IDENTICAL", VersionNo: 1, ComponentKindCode: "RBC",
		RulePriority: 1, BasisClause: "демо-набор, правило 1", ValidFrom: day(2026, 1, 1),
	},
	{
		// Версия 1 закрыта: показывает, что решение по заявке ссылается на версию, действовавшую на момент заявки.
		RuleCode: "DEMO_RBC_O_NEG", VersionNo: 1, ComponentKindCode: "RBC", AllowedGroupCode: "O-",
		ApplicationCondition: "URGENCY=URGENT|EMERGENCY",
		RulePriority:         3, BasisClause: "демо-набор, правило 2", ValidFrom: day(2026, 1, 1), ValidTo: day(2026, 7, 1),
	},
	{
		RuleCode: "DEMO_RBC_O_NEG", VersionNo: 2, ComponentKindCode: "RBC", AllowedGroupCode: "O-",
		ApplicationCondition: "URGENCY=EMERGENCY",
		RulePriority:         3, BasisClause: "демо-набор, правило 2", ValidFrom: day(2026, 7, 1),
	},
	{
		RuleCode: "DEMO_RBC_A_NEG_FOR_A_POS", VersionNo: 1, ComponentKindCode: "RBC",
		RequestedGroupCode: "A+", AllowedGroupCode: "A-",
		RulePriority: 2, BasisClause: "демо-набор, правило 3", ValidFrom: day(2026, 1, 1),
	},
	{
		RuleCode: "DEMO_RBC_B_NEG_FOR_B_POS", VersionNo: 1, ComponentKindCode: "RBC",
		RequestedGroupCode: "B+", AllowedGroupCode: "B-",
		RulePriority: 2, BasisClause: "демо-набор, правило 4", ValidFrom: day(2026, 1, 1),
	},
	{
		RuleCode: "DEMO_FFP_IDENTICAL", VersionNo: 1, ComponentKindCode: "FFP",
		RulePriority: 1, BasisClause: "демо-набор, правило 5", ValidFrom: day(2026, 1, 1),
	},
	{
		RuleCode: "DEMO_FFP_AB", VersionNo: 1, ComponentKindCode: "FFP", AllowedGroupCode: "AB+",
		RulePriority: 2, BasisClause: "демо-набор, правило 6", ValidFrom: day(2026, 1, 1),
	},
	{
		RuleCode: "DEMO_PLT_IDENTICAL", VersionNo: 1, ComponentKindCode: "PLT",
		RulePriority: 1, BasisClause: "демо-набор, правило 7", ValidFrom: day(2026, 1, 1),
	},
	{
		RuleCode: "DEMO_CRYO_IDENTICAL", VersionNo: 1, ComponentKindCode: "CRYO",
		RulePriority: 1, BasisClause: "демо-набор, правило 8", ValidFrom: day(2026, 1, 1),
	},
	{
		// Исключение: плановая заявка не расходует замену DEMO_FFP_AB.
		RuleCode: "DEMO_FFP_AB_PLANNED_EXC", VersionNo: 1, ComponentKindCode: "FFP", AllowedGroupCode: "AB+",
		ApplicationCondition: "URGENCY=PLANNED", IsException: true,
		RulePriority: 1, BasisClause: "демо-набор, правило 9", ValidFrom: day(2026, 1, 1),
	},
}

// DemoMatchingRules возвращает копию демо-набора O15 для сида справочника и тестов подбора.
// ДЕМО: синтетические правила, не клинические.
func DemoMatchingRules() []MatchingRule {
	out := make([]MatchingRule, len(demoRules))
	copy(out, demoRules)
	for i := range out {
		out[i].BasisDocument = DemoRulesBasisDocument
	}
	return out
}
