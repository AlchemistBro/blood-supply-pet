package domain

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
)

// AlgorithmVersion — версия правил расчёта плана; входит в версии входов решения (FR035, NFR009).
const AlgorithmVersion = "planning-greedy-1 (демо)"

// DeficitReasonNoSource — причина дефицита плана из словаря deficit_record.deficit_reason (data-model.md):
// для непокрытой части нет подходящего источника.
const DeficitReasonNoSource = "NO_SOURCE"

// TargetStock — целевой запас клиники O06 по виду и группе (FR021, FR023) и итог подбора для неё.
type TargetStock struct {
	ClinicID        int64
	Kind            string
	Group           string
	NormML          int64
	BackgroundML    int64
	EmergencyML     int64
	TargetML        int64
	IsRoughEstimate bool
	// StockML — собственный запас (доступные единицы минус обещанные по подтверждённым обязательствам).
	StockML     int64
	InboundML   int64
	NeedML      int64
	ProposedML  int64
	UncoveredML int64
}

// ProtectedVolume — защищённый объём источника по виду (FR025) и его остаток после плана (NFR013).
// Публикуется событием ProtectedVolumeCalculated (контракт — пакет S5-WP0).
type ProtectedVolume struct {
	ClinicID       int64
	Kind           string
	NormML         int64
	BackgroundML   int64
	ObligationsML  int64
	VolumeML       int64
	VolumeUnits    int64
	AvailableML    int64
	AvailableUnits int64
	RemainingML    int64
	RemainingUnits int64
}

// Proposal — предложение передачи (TransferProposed → OpenObligation, FR028). Основной источник и
// запасные для эскалации при неподтверждении в срок (fallback_sources).
type Proposal struct {
	ProposalID      uuid.UUID
	SourceClinicID  int64
	TargetClinicID  int64
	Kind            string
	Group           string
	Units           int
	VolumeML        int64
	UnitIDs         []uuid.UUID
	DeliveryMinutes int
	FallbackSources []int64
	Explanation     Explanation
}

// Deficit — запись реестра дефицита O11 типа PLAN (FR027, NFR014).
type Deficit struct {
	DeficitID   uuid.UUID
	ClinicID    int64
	Kind        string
	Group       string
	UncoveredML int64
	Reason      string
	Detail      string
}

// PlanInputs — версии входов, по которым план воспроизводится (FR035, NFR009).
type PlanInputs struct {
	InputVersions
	ForecastID       uuid.UUID
	ScenarioVersion  string
	OldestEstimateAt time.Time
	AlgorithmVersion string
}

// Plan — результат расчёта: предварительный план пополнения (допущение А2: только предложения,
// каждую передачу подтверждает оператор клиники-источника).
type Plan struct {
	PlanID    uuid.UUID
	CreatedAt time.Time
	Inputs    PlanInputs
	Params    Params
	Emergency []EmergencyAllocation
	Targets   []TargetStock
	Protected []ProtectedVolume
	Proposals []Proposal
	Deficits  []Deficit
	NeedML    int64
	CoveredML int64
	// CoverageRatio — доля покрытой потребности, 4 знака (numeric в replenishment_plan.coverage_ratio).
	CoverageRatio  float64
	BelowThreshold bool
}

// Planner строит план пополнения. Время расчёта — из Clock: от него зависят годность единиц
// на момент прибытия и возраст оценок доставки.
type Planner struct {
	clock clock.Clock
}

// NewPlanner создаёт планировщик.
func NewPlanner(c clock.Clock) *Planner { return &Planner{clock: c} }

// Build рассчитывает план. Алгоритм жадный:
//  1. Ячейки «клиника × вид × группа» с потребностью упорядочиваются: сначала со сценарием ЧС, затем по
//     убыванию потребности, затем по ключу.
//  2. Для ячейки кандидаты-источники — другие клиники с доступными единицами той же группы, оценкой доставки,
//     остатком сверх защищённого объёма и сверх собственного целевого запаса группы, с единицей, годной на
//     момент прибытия. Ранжирование (FR026): время доставки, затем больший избыток, затем код клиники.
//  3. Единицы берутся по FEFO, пока потребность не покрыта; единица не делится, предложение не превышает
//     потребность; каждая единица проверяется на защищённый объём источника в мл и в единицах (NFR013).
//  4. Непокрытое — запись дефицита (NFR014), запасные источники — следующие пригодные кандидаты.
func (p *Planner) Build(in Input) (*Plan, error) {
	now := p.clock.Now().UTC()
	st, err := prepare(in, now)
	if err != nil {
		return nil, err
	}
	plan := &Plan{PlanID: in.PlanID, CreatedAt: now, Params: in.Params, Emergency: st.emergency}
	plan.Inputs = PlanInputs{InputVersions: in.Versions, AlgorithmVersion: AlgorithmVersion}
	if in.Forecast != nil {
		plan.Inputs.ForecastID, plan.Inputs.ScenarioVersion = in.Forecast.ForecastID, in.Forecast.ScenarioVersion
	}
	for _, d := range in.Delivery {
		if plan.Inputs.OldestEstimateAt.IsZero() || d.EstimatedAt.Before(plan.Inputs.OldestEstimateAt) {
			plan.Inputs.OldestEstimateAt = d.EstimatedAt
		}
	}

	order := make([]*cell, 0, len(st.cellKeys))
	for _, k := range st.cellKeys {
		if c := st.cells[k]; c.needML() > 0 {
			order = append(order, c)
		}
	}
	slices.SortStableFunc(order, func(a, b *cell) int {
		ae, be := a.emergencyML > 0, b.emergencyML > 0
		if ae != be {
			if ae {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(b.needML(), a.needML()), compareCellKey(a.key, b.key))
	})

	needs := make(map[cellKey]int64, len(order))
	for _, c := range order {
		need := c.needML()
		needs[c.key] = need
		proposals, deficit := st.fill(in.PlanID, c, need)
		plan.Proposals = append(plan.Proposals, proposals...)
		if deficit != nil {
			plan.Deficits = append(plan.Deficits, *deficit)
		}
		plan.NeedML += need
		plan.CoveredML += c.proposedML
	}

	for i := range plan.Proposals {
		pr := &plan.Proposals[i]
		s := st.sources[kindKey{clinic: pr.SourceClinicID, kind: pr.Kind}]
		pr.Explanation.RemainingAfterPlanML = s.availableML - s.donatedML
		pr.Explanation.RemainingAfterPlanUnits = s.availableUnits - s.donatedUnits
	}
	plan.Targets = st.targets(needs)
	plan.Protected = st.protectedVolumes()
	plan.CoverageRatio = 1
	if plan.NeedML > 0 {
		plan.CoverageRatio = math.Round(float64(plan.CoveredML)/float64(plan.NeedML)*1e4) / 1e4
	}
	plan.BelowThreshold = plan.CoverageRatio < in.Params.CoverageThreshold
	return plan, nil
}

// candidate — источник, пригодный для ячейки-получателя.
type candidate struct {
	donor     *cell
	src       *source
	estimate  DeliveryEstimate
	threshold time.Time
	surplus   int64
}

// Exclusions — сколько клиник с единицами нужной группы отсеяно и почему (для объяснения и дефицита).
type Exclusions struct {
	NoEstimate   int
	Protected    int
	OwnTarget    int
	NoFreshUnits int
}

// hasFreshUnit — есть ли невзятая единица, годная к порогу (единицы упорядочены по сроку: смотрим с конца).
func (c *cell) hasFreshUnit(threshold time.Time) bool {
	for i := len(c.units) - 1; i >= 0; i-- {
		if c.taken[i] {
			continue
		}
		return !c.units[i].ExpiresAt.Before(threshold)
	}
	return false
}

func (st *state) viable(c candidate) bool {
	return c.src.hasSpare() && c.donor.surplusML() > 0 && c.donor.hasFreshUnit(c.threshold)
}

// candidates отбирает и ранжирует источники для ячейки получателя (FR026).
func (st *state) candidates(target *cell) ([]candidate, Exclusions) {
	var (
		out []candidate
		ex  Exclusions
	)
	for _, clinic := range st.donors[[2]string{target.key.kind, target.key.group}] {
		if clinic == target.key.clinic {
			continue
		}
		donor := st.cells[cellKey{clinic: clinic, kind: target.key.kind, group: target.key.group}]
		src := st.sources[kindKey{clinic: clinic, kind: target.key.kind}]
		est, ok := st.routes[[2]int64{clinic, target.key.clinic}]
		switch {
		case !ok:
			ex.NoEstimate++
			continue
		case !src.hasSpare():
			ex.Protected++
			continue
		case donor.surplusML() <= 0:
			ex.OwnTarget++
			continue
		}
		cd := candidate{
			donor: donor, src: src, estimate: est, surplus: donor.surplusML(),
			threshold: st.now.Add(time.Duration(est.Minutes)*time.Minute + st.params.MinShelfLifeAtArrival),
		}
		if !donor.hasFreshUnit(cd.threshold) {
			ex.NoFreshUnits++
			continue
		}
		out = append(out, cd)
	}
	slices.SortStableFunc(out, func(a, b candidate) int {
		return cmp.Or(cmp.Compare(a.estimate.Minutes, b.estimate.Minutes), cmp.Compare(b.surplus, a.surplus),
			cmp.Compare(a.donor.key.clinic, b.donor.key.clinic))
	})
	return out, ex
}

// fill покрывает потребность ячейки из ранжированных источников и возвращает предложения и запись дефицита.
func (st *state) fill(planID uuid.UUID, target *cell, need int64) ([]Proposal, *Deficit) {
	cands, ex := st.candidates(target)
	var (
		proposals []Proposal
		covered   int64
	)
	for rank, cd := range cands {
		if covered >= need {
			break
		}
		var (
			ids      []uuid.UUID
			volume   int64
			earliest time.Time
		)
		for i, u := range cd.donor.units {
			if covered >= need {
				break
			}
			if cd.donor.taken[i] || u.ExpiresAt.Before(cd.threshold) || covered+u.VolumeML > need {
				continue
			}
			if !cd.src.canGive(u.VolumeML) || cd.donor.surplusML()-u.VolumeML < 0 {
				continue
			}
			cd.donor.taken[i] = true
			cd.donor.donatedML += u.VolumeML
			cd.src.donatedML += u.VolumeML
			cd.src.donatedUnits++
			covered += u.VolumeML
			volume += u.VolumeML
			ids = append(ids, u.UnitID)
			if earliest.IsZero() || u.ExpiresAt.Before(earliest) {
				earliest = u.ExpiresAt
			}
		}
		if len(ids) == 0 {
			continue
		}
		k := target.key
		proposals = append(proposals, Proposal{
			ProposalID:     uuid.NewSHA1(planID, fmt.Appendf(nil, "proposal:%d:%d:%s:%s", cd.donor.key.clinic, k.clinic, k.kind, k.group)),
			SourceClinicID: cd.donor.key.clinic, TargetClinicID: k.clinic, Kind: k.kind, Group: k.group,
			Units: len(ids), VolumeML: volume, UnitIDs: ids, DeliveryMinutes: cd.estimate.Minutes,
			Explanation: Explanation{
				RankingRule: RankingRule, Candidates: len(cands), Rank: rank + 1, Excluded: ex,
				Chosen:    st.factors(cd),
				ArrivalAt: st.now.Add(time.Duration(cd.estimate.Minutes) * time.Minute), EarliestExpiry: earliest,
				NormML: cd.src.normML, BackgroundML: cd.src.backgroundML, ObligationsML: cd.src.obligationsML,
				ProtectedML: cd.src.protectedML(), ProtectedUnits: cd.src.protectedUnits(),
			},
		})
	}
	target.proposedML = covered

	// Запасные источники: пригодные после подбора кандидаты в порядке ранга, кроме основного.
	var spare []int64
	for _, cd := range cands {
		if st.viable(cd) {
			spare = append(spare, cd.donor.key.clinic)
		}
	}
	for i := range proposals {
		fb := make([]int64, 0, st.params.MaxFallbackSources)
		for _, c := range spare {
			if len(fb) == st.params.MaxFallbackSources {
				break
			}
			if c != proposals[i].SourceClinicID {
				fb = append(fb, c)
			}
		}
		proposals[i].FallbackSources = fb
	}

	if covered >= need {
		return proposals, nil
	}
	k := target.key
	return proposals, &Deficit{
		DeficitID: uuid.NewSHA1(planID, fmt.Appendf(nil, "deficit:%d:%s:%s", k.clinic, k.kind, k.group)),
		ClinicID:  k.clinic, Kind: k.kind, Group: k.group, UncoveredML: need - covered,
		Reason: DeficitReasonNoSource, Detail: deficitDetail(len(cands), ex, covered, need),
	}
}

func (st *state) factors(cd candidate) SourceFactors {
	age := st.now.Sub(cd.estimate.EstimatedAt)
	return SourceFactors{
		ClinicID: cd.donor.key.clinic, DeliveryMinutes: cd.estimate.Minutes, EstimatedAt: cd.estimate.EstimatedAt,
		EstimateAge: age, EstimateStale: age > st.params.EstimateStaleAfter,
		AvailableML: cd.src.availableML, AvailableUnits: cd.src.availableUnits, GroupSurplusML: cd.surplus,
	}
}

// targets собирает целевой запас по всем ячейкам, где есть цель или запас.
func (st *state) targets(needs map[cellKey]int64) []TargetStock {
	out := make([]TargetStock, 0, len(st.cellKeys))
	for _, k := range st.cellKeys {
		c := st.cells[k]
		if c.targetML() == 0 && c.availableML == 0 && c.inboundML == 0 {
			continue
		}
		need := needs[k]
		out = append(out, TargetStock{
			ClinicID: k.clinic, Kind: k.kind, Group: k.group,
			NormML: c.normML, BackgroundML: c.backgroundML, EmergencyML: c.emergencyML, TargetML: c.targetML(),
			IsRoughEstimate: c.rough, StockML: c.stockML(), InboundML: c.inboundML,
			NeedML: need, ProposedML: c.proposedML, UncoveredML: need - c.proposedML,
		})
	}
	return out
}

// protectedVolumes — защищённый объём и остаток по всем остаткам источников.
func (st *state) protectedVolumes() []ProtectedVolume {
	keys := make([]kindKey, 0, len(st.sources))
	for k := range st.sources {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, compareKindKey)
	out := make([]ProtectedVolume, 0, len(keys))
	for _, k := range keys {
		s := st.sources[k]
		out = append(out, ProtectedVolume{
			ClinicID: k.clinic, Kind: k.kind, NormML: s.normML, BackgroundML: s.backgroundML, ObligationsML: s.obligationsML,
			VolumeML: s.protectedML(), VolumeUnits: s.protectedUnits(),
			AvailableML: s.availableML, AvailableUnits: s.availableUnits,
			RemainingML: s.availableML - s.donatedML, RemainingUnits: s.availableUnits - s.donatedUnits,
		})
	}
	return out
}
