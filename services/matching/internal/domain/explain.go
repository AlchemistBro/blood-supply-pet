package domain

import (
	"fmt"
	"strings"
	"time"
)

// demoMark открывает каждое объяснение. Система не устанавливает индивидуальную совместимость и не принимает
// клиническое решение: она показывает, каким синтетическим правилам соответствует вариант (ADR-0010;
// ПП РФ № 641, п. 89–94 — подбор выполняет медицинская организация).
const demoMark = "ДЕМО: правила подбора и данные синтетические, не клинические; вариант выбирает врач."

// timeLayout — время в объяснении; часовой пояс UTC указан явно.
const timeLayout = "2006-01-02 15:04 UTC"

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func yesNo(b bool) string {
	if b {
		return "да"
	}
	return "нет"
}

// explainVariant — почему вариант допустим и почему у него такое место (NFR009, FR034).
// Требования фенотипа в текст не попадают: объяснение показывается и хранится, фенотип — квазиидентификатор.
func explainVariant(res Result, req Request, policy Policy, v Variant, prev *Variant, total int) string {
	var b strings.Builder
	b.WriteString(demoMark)
	b.WriteString("\n")

	// Почему допустим.
	fmt.Fprintf(&b, "Почему допустим: соответствует правилу демо-набора %s версии %d (приоритет %d; основание: %s, %s). ",
		v.Rule.Code, v.Rule.Version, v.Rule.Priority, v.Rule.BasisDocument, v.Rule.BasisClause)
	if v.IsPreferred {
		fmt.Fprintf(&b, "Предпочтительный вариант: вид %s и группа %s совпадают с заявкой.\n", v.ComponentKind, v.BloodGroup)
	} else {
		fmt.Fprintf(&b, "Замена %s %s → %s %s разрешена этим правилом; действующие исключения её не запрещают.\n",
			req.ComponentKind, req.BloodGroup, v.ComponentKind, v.BloodGroup)
	}
	b.WriteString("Ограничения заявки соблюдены: ")
	checks := []string{"только единицы в наличии", "годность на момент прибытия"}
	if req.Phenotype != "" {
		checks = append(checks, "требования фенотипа")
	}
	for _, c := range req.Constraints {
		checks = append(checks, c.Code)
	}
	b.WriteString(strings.Join(checks, ", "))
	b.WriteString(".\n")

	// Факторы.
	cover := "заявка покрыта полностью"
	if !v.CoversRequest {
		cover = "заявка покрыта частично"
	}
	fmt.Fprintf(&b, "Единицы: подобрано %d из %d запрошенных, у источника доступно %d (%s).\n",
		len(v.UnitIDs), req.RequestedUnits, v.AvailableUnits, cover)
	fmt.Fprintf(&b, "Годность: ближайшая до %s; на момент прибытия остаётся %d ч.\n",
		formatTime(v.EarliestExpiresAt), int64(v.EarliestExpiresAt.Sub(v.ArrivalAt)/time.Hour))
	switch v.Delivery.Status {
	case DeliveryLocal:
		b.WriteString("Время получения: местный вариант, 0 мин.\n")
	case DeliveryEstimated:
		fmt.Fprintf(&b, "Время получения: %d мин, оценка C03 от %s, возраст %s.\n",
			v.Delivery.Minutes, formatTime(v.Delivery.EstimatedAt), formatDuration(v.Delivery.Age))
	case DeliveryStale:
		age := "время оценки неизвестно"
		if !v.Delivery.EstimatedAt.IsZero() {
			age = fmt.Sprintf("оценка C03 от %s, возраст %s", formatTime(v.Delivery.EstimatedAt), formatDuration(v.Delivery.Age))
		}
		fmt.Fprintf(&b, "Время получения: %d мин; %s — оценка устарела (порог %s, демо-параметр, ADR-0014). Она используется для проверки годности, при равном времени вариант идёт после вариантов со свежей оценкой.\n",
			v.Delivery.Minutes, age, formatDuration(policy.EstimateStaleAfter))
	case DeliveryUnknown:
		fmt.Fprintf(&b, "Время получения: оценки нет; для проверки годности принято %d мин (демо-параметр), в ранжировании вариант идёт после вариантов с оценкой.\n",
			policy.UnknownDeliveryMinutes)
	}
	switch {
	case v.IsLocal:
		b.WriteString("Защищённый объём: местный вариант, передачи нет.\n")
	case !v.ProtectedKnown:
		b.WriteString("Защищённый объём источника: сведений нет.\n")
	default:
		spend := "не расходует защищённый запас"
		if v.IsDeficitStockUsed {
			spend = fmt.Sprintf("расходует защищённый запас: %d ед.", v.Protected.ConsumedUnits)
		}
		fmt.Fprintf(&b, "Защищённый объём источника: после передачи останется %d ед. при защищённом %d ед. — %s.\n",
			v.Protected.RemainingAfterUnits, v.Protected.ProtectedUnits, spend)
	}

	// Почему такое место.
	if prev == nil {
		fmt.Fprintf(&b, "Место %d из %d: порядок факторов — %s; шаг сравнения годности %s (демо-параметр).\n",
			v.Rank, total, factorNames(), formatDuration(policy.ExpiryBucket))
	} else if f, ok := decidingFactor(*prev, v); ok {
		fmt.Fprintf(&b, "Место %d из %d: ниже места %d (клиника %d, %s %s): решил фактор «%s» (%s против %s).\n",
			v.Rank, total, prev.Rank, prev.SourceClinicID, prev.ComponentKind, prev.BloodGroup, f.name, f.show(*prev), f.show(v))
	}

	writeInputs(&b, res)
	return strings.TrimRight(b.String(), "\n")
}

// explainDeficit — почему нет ни одного допустимого варианта.
func explainDeficit(res Result, req Request) string {
	var b strings.Builder
	b.WriteString(demoMark)
	b.WriteString("\n")
	fmt.Fprintf(&b, "Дефицит (%s): нет допустимого варианта для %d ед. %s %s. Ограничения заявки не ослабляются из-за нехватки запаса.\n",
		DeficitNoAllowedVariant, req.RequestedUnits, req.ComponentKind, req.BloodGroup)
	if len(res.Notes) > 0 {
		b.WriteString("Причины: ")
		b.WriteString(strings.Join(res.Notes, "; "))
		b.WriteString(".\n")
	} else {
		b.WriteString("Причины: единиц, соответствующих правилам демо-набора и ограничениям заявки, в наличии нет.\n")
	}
	if len(req.Constraints) > 0 {
		codes := make([]string, 0, len(req.Constraints))
		for _, c := range req.Constraints {
			codes = append(codes, c.Code)
		}
		fmt.Fprintf(&b, "Ограничения заявки: %s.\n", strings.Join(codes, ", "))
	}
	writeInputs(&b, res)
	return strings.TrimRight(b.String(), "\n")
}

// writeInputs — версии входов решения (NFR009, FR035).
func writeInputs(b *strings.Builder, res Result) {
	rules := make([]string, 0, len(res.RulesUsed))
	for _, r := range res.RulesUsed {
		rules = append(rules, fmt.Sprintf("%s v%d", r.Code, r.Version))
	}
	if len(rules) == 0 {
		rules = append(rules, "нет действующих")
	}
	fmt.Fprintf(b, "Входы: запас версии %d на %s; подбор на %s; правила на момент заявки %s: %s.\n",
		res.StockWatermark, formatTime(res.StockSyncedAt), formatTime(res.MatchedAt), formatTime(res.RulesAt),
		strings.Join(rules, ", "))
}

func factorNames() string {
	names := make([]string, 0, len(rankFactors))
	for _, f := range rankFactors {
		names = append(names, f.name)
	}
	return strings.Join(names, " → ")
}

// formatDuration — длительность в часах и минутах: «6 ч», «7 ч 5 мин», «40 мин».
func formatDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	h, m := int64(d/time.Hour), int64(d%time.Hour/time.Minute)
	switch {
	case h == 0:
		return fmt.Sprintf("%d мин", m)
	case m == 0:
		return fmt.Sprintf("%d ч", h)
	default:
		return fmt.Sprintf("%d ч %d мин", h, m)
	}
}
