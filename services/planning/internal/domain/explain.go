package domain

import (
	"fmt"
	"strings"
	"time"
)

// RankingRule — правило сравнения источников (FR026), попадает в объяснение каждого предложения.
const RankingRule = "время доставки → избыток группы над собственным целевым запасом → код клиники"

// SourceFactors — факторы источника, повлиявшие на выбор (NFR009).
type SourceFactors struct {
	ClinicID        int64
	DeliveryMinutes int
	EstimatedAt     time.Time
	EstimateAge     time.Duration
	// EstimateStale — оценка старше порога: используется, но помечается «устарела» (ADR-0014).
	EstimateStale bool
	// AvailableML и AvailableUnits — подтверждённый запас вида у источника по снимку (FR026).
	AvailableML    int64
	AvailableUnits int64
	// GroupSurplusML — избыток группы над собственным целевым запасом источника до выбора.
	GroupSurplusML int64
}

// Explanation — почему для предложения выбран этот источник (NFR009). Версии входов — в PlanInputs плана.
type Explanation struct {
	RankingRule    string
	Candidates     int
	Rank           int
	Excluded       Exclusions
	Chosen         SourceFactors
	ArrivalAt      time.Time
	EarliestExpiry time.Time
	NormML         int64
	BackgroundML   int64
	ObligationsML  int64
	ProtectedML    int64
	ProtectedUnits int64
	// RemainingAfterPlanML и RemainingAfterPlanUnits — остаток вида у источника после всех предложений плана.
	RemainingAfterPlanML    int64
	RemainingAfterPlanUnits int64
}

const timeLayout = "2006-01-02 15:04 UTC"

// Text — объяснение человеческим языком для экрана «Почему этот источник» и поля transfer_proposal.explanation.
func (e Explanation) Text() string {
	var b strings.Builder
	c := e.Chosen
	fmt.Fprintf(&b, "Источник — клиника %d: ранг %d из %d по правилу «%s». ", c.ClinicID, e.Rank, e.Candidates, e.RankingRule)
	fmt.Fprintf(&b, "Время доставки %d мин (оценка от %s, возраст %s", c.DeliveryMinutes, c.EstimatedAt.UTC().Format(timeLayout),
		c.EstimateAge.Round(time.Minute))
	if c.EstimateStale {
		b.WriteString(", оценка устарела")
	}
	b.WriteString("). ")
	fmt.Fprintf(&b, "Подтверждённый запас вида у источника %d мл (%d ед.), избыток группы %d мл; ",
		c.AvailableML, c.AvailableUnits, c.GroupSurplusML)
	fmt.Fprintf(&b, "защищённый объём %d мл (%d ед.): норматив %d + фон %d + подтверждённые обязательства %d; после плана остаётся %d мл (%d ед.). ",
		e.ProtectedML, e.ProtectedUnits, e.NormML, e.BackgroundML, e.ObligationsML, e.RemainingAfterPlanML, e.RemainingAfterPlanUnits)
	fmt.Fprintf(&b, "Прибытие %s, ближайший срок годности переданных единиц %s. ",
		e.ArrivalAt.UTC().Format(timeLayout), e.EarliestExpiry.UTC().Format(timeLayout))
	if ex := e.Excluded.text(); ex != "" {
		fmt.Fprintf(&b, "Отсеяно: %s. ", ex)
	}
	b.WriteString("Нормативы и параметры — демо (ADR-0010).")
	return b.String()
}

func (x Exclusions) text() string {
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", what, n))
		}
	}
	add(x.NoEstimate, "без оценки доставки")
	add(x.Protected, "на пределе защищённого объёма")
	add(x.OwnTarget, "без избытка над своим целевым запасом")
	add(x.NoFreshUnits, "без единиц, годных на момент прибытия")
	return strings.Join(parts, ", ")
}

// deficitDetail — пояснение к записи дефицита: сколько источников было и почему потребность не покрыта.
func deficitDetail(candidates int, ex Exclusions, covered, need int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Покрыто %d из %d мл; пригодных источников %d", covered, need, candidates)
	if t := ex.text(); t != "" {
		fmt.Fprintf(&b, "; отсеяно: %s", t)
	}
	if candidates > 0 {
		b.WriteString("; запас пригодных источников исчерпан или остаток потребности меньше свободной единицы (единица не делится)")
	}
	b.WriteString(".")
	return b.String()
}
