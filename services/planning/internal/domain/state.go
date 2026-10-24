package domain

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
)

// cellKey — ячейка расчёта: клиника × вид × группа (для вида без группы группа пустая).
type cellKey struct {
	clinic int64
	kind   string
	group  string
}

func compareCellKey(a, b cellKey) int {
	return cmp.Or(cmp.Compare(a.clinic, b.clinic), cmp.Compare(a.kind, b.kind), cmp.Compare(a.group, b.group))
}

// kindKey — остаток источника: клиника × вид. На этом уровне считается защищённый объём —
// так же, как его проверяет transfer в source_stock (obligation-states.md, NFR013).
type kindKey struct {
	clinic int64
	kind   string
}

func compareKindKey(a, b kindKey) int {
	return cmp.Or(cmp.Compare(a.clinic, b.clinic), cmp.Compare(a.kind, b.kind))
}

// cell — целевой запас и запас клиники по виду и группе.
type cell struct {
	key          cellKey
	normML       int64
	backgroundML int64
	emergencyML  int64
	rough        bool
	// Доступные единицы: статус IN_STOCK и срок годности позже момента расчёта; порядок FEFO (FR015).
	units       []Unit
	taken       []bool
	availableML int64
	// Подтверждённые обязательства, где клиника источник (единицы ещё числятся у неё) и получатель.
	outgoingML int64
	inboundML  int64
	donatedML  int64
	// Итог подбора для клиники как получателя.
	proposedML int64
}

// targetML — целевой запас O06 (FR021, FR023): норматив + фон + доля прогноза ЧС.
func (c *cell) targetML() int64 { return c.normML + c.backgroundML + c.emergencyML }

// stockML — собственный запас клиники: доступные единицы за вычетом обещанных по подтверждённым обязательствам.
// Не меньше нуля: если снимок запаса уже не содержит обещанных единиц (отправлены), мнимой потребности нет.
func (c *cell) stockML() int64 { return max(0, c.availableML-c.outgoingML) }

// needML — потребность пополнения: цель минус запас и уже подтверждённые входящие передачи.
func (c *cell) needML() int64 { return max(0, c.targetML()-c.stockML()-c.inboundML) }

// surplusML — сколько клиника может отдать по этой группе, не опускаясь ниже своего целевого запаса.
// Клиника с потребностью по группе источником для неё не бывает: нет встречных передач.
func (c *cell) surplusML() int64 { return c.stockML() - c.donatedML - c.targetML() }

// source — остаток источника по виду и его защищённый объём (FR025).
type source struct {
	key              kindKey
	typicalML        int64
	normML           int64
	backgroundML     int64
	obligationsML    int64
	obligationsUnits int64
	availableML      int64
	availableUnits   int64
	donatedML        int64
	donatedUnits     int64
}

// protectedML — защищённый объём источника (FR025, NFR013): норматив + фоновая потребность источника
// за период + подтверждённые обязательства. Внутренний порог плана в мл.
func (s *source) protectedML() int64 { return s.normML + s.backgroundML + s.obligationsML }

// publishedUnits — защищённый объём для transfer (ADR-0019): ceil((норматив + фон) / типовой объём),
// без подтверждённых обязательств — transfer сам вычитает закреплённые единицы (free = available − reserved),
// иначе обязательства вычлись бы дважды.
func (s *source) publishedUnits() int64 {
	return (s.normML + s.backgroundML + s.typicalML - 1) / s.typicalML
}

// protectedUnits — внутренний порог плана в единицах: опубликованный защищённый объём плюс единицы
// подтверждённых обязательств. Условие available − donated ≥ protectedUnits равносильно проверке transfer
// (available − reserved) − k ≥ published (obligation-states.md, шаг 5), поэтому план не предлагает того,
// что transfer отклонит как ProtectedVolumeExceeded.
func (s *source) protectedUnits() int64 { return s.publishedUnits() + s.obligationsUnits }

// canGive проверяет, что после передачи ещё одной единицы объёмом v остаток не ниже защищённого объёма
// ни в мл, ни в единицах.
func (s *source) canGive(v int64) bool {
	return s.availableML-s.donatedML-v >= s.protectedML() &&
		s.availableUnits-s.donatedUnits-1 >= s.protectedUnits()
}

// hasSpare — у источника есть хоть что-то сверх защищённого объёма.
func (s *source) hasSpare() bool {
	return s.availableML-s.donatedML > s.protectedML() && s.availableUnits-s.donatedUnits > s.protectedUnits()
}

// heldOutgoing — статусы O07, при которых единицы обещаны и числятся у источника: «подтверждённые
// обязательства» FR025. PENDING не в счёт: до подтверждения единицы не блокируются (FR048).
func heldOutgoing(status string) bool {
	switch status {
	case ObligationConfirmed, ObligationTransportRequested, ObligationDispatched:
		return true
	default:
		return false
	}
}

// state — подготовленный вход расчёта.
type state struct {
	now       time.Time
	params    Params
	kinds     map[string]Kind
	groups    []GroupShare
	emergency []EmergencyAllocation
	cells     map[cellKey]*cell
	cellKeys  []cellKey
	sources   map[kindKey]*source
	// donors — клиники с доступными единицами по виду и группе, по возрастанию идентификатора.
	donors map[[2]string][]int64
	routes map[[2]int64]DeliveryEstimate
}

func (st *state) cell(k cellKey) *cell {
	c, ok := st.cells[k]
	if !ok {
		c = &cell{key: k}
		st.cells[k] = c
	}
	return c
}

func (st *state) source(k kindKey) *source {
	s, ok := st.sources[k]
	if !ok {
		s = &source{key: k, typicalML: st.kinds[k.kind].TypicalVolumeML}
		st.sources[k] = s
	}
	return s
}

func (st *state) groupKey(kind Kind, group string) string {
	if !kind.GroupApplicable {
		return ""
	}
	return group
}

func validateParams(p Params) error {
	if !(p.CoverageThreshold > 0 && p.CoverageThreshold <= 1) {
		return fmt.Errorf("%w: порог покрытия %v вне (0, 1]", ErrInvalidInput, p.CoverageThreshold)
	}
	if p.MinShelfLifeAtArrival < 0 || p.MaxFallbackSources < 0 || p.EstimateStaleAfter < 0 {
		return fmt.Errorf("%w: отрицательный параметр расчёта", ErrInvalidInput)
	}
	return nil
}

// prepare проверяет вход и раскладывает его по ячейкам и остаткам источников. Все обходы — по
// отсортированным ключам: результат не зависит от порядка элементов во входе.
func prepare(in Input, now time.Time) (*state, error) {
	if in.PlanID == uuid.Nil {
		return nil, fmt.Errorf("%w: нет идентификатора плана", ErrInvalidInput)
	}
	if err := validateParams(in.Params); err != nil {
		return nil, err
	}
	st := &state{
		now: now, params: in.Params, kinds: map[string]Kind{}, cells: map[cellKey]*cell{},
		sources: map[kindKey]*source{}, donors: map[[2]string][]int64{}, routes: map[[2]int64]DeliveryEstimate{},
	}
	for _, k := range in.Kinds {
		if k.Code == "" || k.TypicalVolumeML <= 0 {
			return nil, fmt.Errorf("%w: вид %q без кода или типового объёма", ErrInvalidInput, k.Code)
		}
		if _, dup := st.kinds[k.Code]; dup {
			return nil, fmt.Errorf("%w: вид %s повторяется", ErrInvalidInput, k.Code)
		}
		st.kinds[k.Code] = k
	}
	groups, err := sortedGroups(in.GroupShares)
	if err != nil {
		return nil, err
	}
	st.groups = groups
	kindOf := func(code, what string) (Kind, error) {
		k, ok := st.kinds[code]
		if !ok {
			return Kind{}, fmt.Errorf("%w: %s: неизвестный вид %q", ErrInvalidInput, what, code)
		}
		return k, nil
	}

	st.emergency, err = distributeForecast(in.Forecast, in.ClinicShares, st.kinds, groups)
	if err != nil {
		return nil, err
	}
	for _, a := range st.emergency {
		c := st.cell(cellKey{clinic: a.ClinicID, kind: a.Kind, group: a.Group})
		c.emergencyML += a.VolumeML
		c.rough = c.rough || a.IsRoughEstimate
	}

	seenNorm := map[kindKey]bool{}
	for _, n := range in.Norms {
		kind, err := kindOf(n.Kind, "норматив")
		if err != nil {
			return nil, err
		}
		kk := kindKey{clinic: n.ClinicID, kind: n.Kind}
		if seenNorm[kk] {
			return nil, fmt.Errorf("%w: норматив клиники %d по виду %s повторяется", ErrInvalidInput, n.ClinicID, n.Kind)
		}
		seenNorm[kk] = true
		if badFloat(n.NormML, maxVolumeML) {
			return nil, fmt.Errorf("%w: норматив клиники %d: %v", ErrInvalidInput, n.ClinicID, n.NormML)
		}
		total := ceilML(n.NormML)
		st.source(kk).normML += total
		parts, _, err := splitGroups(total, kind, "", groups)
		if err != nil {
			return nil, fmt.Errorf("норматив клиники %d: %w", n.ClinicID, err)
		}
		for _, p := range parts {
			st.cell(cellKey{clinic: n.ClinicID, kind: n.Kind, group: p.group}).normML += p.volume
		}
	}

	for _, b := range in.Background {
		kind, err := kindOf(b.Kind, "фоновая потребность")
		if err != nil {
			return nil, err
		}
		if badFloat(b.VolumeML, maxVolumeML) {
			return nil, fmt.Errorf("%w: фоновая потребность клиники %d: %v", ErrInvalidInput, b.ClinicID, b.VolumeML)
		}
		group := st.groupKey(kind, b.Group)
		if err := checkGroup(kind, group, groups); err != nil {
			return nil, fmt.Errorf("фоновая потребность клиники %d: %w", b.ClinicID, err)
		}
		total := roundML(b.VolumeML)
		st.source(kindKey{clinic: b.ClinicID, kind: b.Kind}).backgroundML += total
		parts, _, err := splitGroups(total, kind, group, groups)
		if err != nil {
			return nil, fmt.Errorf("фоновая потребность клиники %d: %w", b.ClinicID, err)
		}
		for _, p := range parts {
			st.cell(cellKey{clinic: b.ClinicID, kind: b.Kind, group: p.group}).backgroundML += p.volume
		}
	}

	reserved, err := st.addObligations(in.Obligations)
	if err != nil {
		return nil, err
	}
	if err := st.addUnits(in.Units, reserved); err != nil {
		return nil, err
	}

	for _, d := range in.Delivery {
		if d.Minutes < 0 || d.Minutes > maxDeliveryMins {
			return nil, fmt.Errorf("%w: время доставки %d → %d = %d мин вне [0, %d]", ErrInvalidInput,
				d.SourceClinicID, d.TargetClinicID, d.Minutes, maxDeliveryMins)
		}
		route := [2]int64{d.SourceClinicID, d.TargetClinicID}
		if _, dup := st.routes[route]; dup {
			return nil, fmt.Errorf("%w: оценка доставки %d → %d повторяется", ErrInvalidInput, d.SourceClinicID, d.TargetClinicID)
		}
		st.routes[route] = d
	}

	st.cellKeys = make([]cellKey, 0, len(st.cells))
	for k := range st.cells {
		st.cellKeys = append(st.cellKeys, k)
	}
	slices.SortFunc(st.cellKeys, compareCellKey)
	for _, k := range st.cellKeys {
		c := st.cells[k]
		if len(c.units) == 0 {
			continue
		}
		slices.SortFunc(c.units, func(a, b Unit) int {
			return cmp.Or(a.ExpiresAt.Compare(b.ExpiresAt), compareUUID(a.UnitID, b.UnitID))
		})
		c.taken = make([]bool, len(c.units))
		dk := [2]string{k.kind, k.group}
		st.donors[dk] = append(st.donors[dk], k.clinic)
	}
	return st, nil
}

// addObligations учитывает подтверждённые обязательства: у источника — в защищённом объёме (мл и единицы)
// и в собственном запасе группы, у получателя — как входящее пополнение. Возвращает единицы, закреплённые
// за этими обязательствами: их нельзя предлагать повторно.
func (st *state) addObligations(obligations []Obligation) (map[uuid.UUID]bool, error) {
	reserved := map[uuid.UUID]bool{}
	seen := map[uuid.UUID]bool{}
	for _, o := range obligations {
		kind, ok := st.kinds[o.Kind]
		if !ok {
			return nil, fmt.Errorf("%w: обязательство %s: неизвестный вид %q", ErrInvalidInput, o.ObligationID, o.Kind)
		}
		if seen[o.ObligationID] {
			return nil, fmt.Errorf("%w: обязательство %s повторяется", ErrInvalidInput, o.ObligationID)
		}
		seen[o.ObligationID] = true
		if o.SourceClinicID == o.TargetClinicID {
			return nil, fmt.Errorf("%w: обязательство %s: источник совпадает с получателем", ErrInvalidInput, o.ObligationID)
		}
		if o.VolumeML < 0 || o.VolumeML > maxVolumeML || o.Units < 0 || o.Units > maxObligationQty {
			return nil, fmt.Errorf("%w: обязательство %s: объём %d мл, единиц %d", ErrInvalidInput, o.ObligationID, o.VolumeML, o.Units)
		}
		units := int64(o.Units)
		if len(o.UnitIDs) > 0 {
			if o.Units != 0 && o.Units != len(o.UnitIDs) {
				return nil, fmt.Errorf("%w: обязательство %s: единиц %d, а закреплено %d", ErrInvalidInput, o.ObligationID, o.Units, len(o.UnitIDs))
			}
			units = int64(len(o.UnitIDs))
		}
		group := st.groupKey(kind, o.Group)
		// Как у единицы: у вида с группой обязательство без группы не отнести ни к одной ячейке запаса.
		if kind.GroupApplicable && group == "" {
			return nil, fmt.Errorf("%w: обязательство %s вида %s без группы", ErrInvalidInput, o.ObligationID, o.Kind)
		}
		if err := checkGroup(kind, group, st.groups); err != nil {
			return nil, fmt.Errorf("обязательство %s: %w", o.ObligationID, err)
		}
		if !heldOutgoing(o.Status) {
			continue
		}
		for _, id := range o.UnitIDs {
			if reserved[id] {
				return nil, fmt.Errorf("%w: единица %s закреплена за двумя обязательствами", ErrInvalidInput, id)
			}
			reserved[id] = true
		}
		st.cell(cellKey{clinic: o.SourceClinicID, kind: o.Kind, group: group}).outgoingML += o.VolumeML
		s := st.source(kindKey{clinic: o.SourceClinicID, kind: o.Kind})
		s.obligationsML += o.VolumeML
		s.obligationsUnits += units
		st.cell(cellKey{clinic: o.TargetClinicID, kind: o.Kind, group: group}).inboundML += o.VolumeML
	}
	return reserved, nil
}

// addUnits раскладывает доступные единицы по ячейкам. Недоступные (карантин, выданные, списанные,
// просроченные к моменту расчёта) в запас не входят и к передаче не предлагаются (ПП РФ № 641, п. 62 а).
// Закреплённые за подтверждёнными обязательствами входят в запас источника, но к передаче не предлагаются.
func (st *state) addUnits(units []Unit, reserved map[uuid.UUID]bool) error {
	seen := make(map[uuid.UUID]bool, len(units))
	for _, u := range units {
		kind, ok := st.kinds[u.Kind]
		if !ok {
			return fmt.Errorf("%w: единица %s: неизвестный вид %q", ErrInvalidInput, u.UnitID, u.Kind)
		}
		if seen[u.UnitID] {
			return fmt.Errorf("%w: единица %s повторяется", ErrInvalidInput, u.UnitID)
		}
		seen[u.UnitID] = true
		if u.VolumeML <= 0 || u.VolumeML > maxUnitVolumeML {
			return fmt.Errorf("%w: единица %s: объём %d мл вне (0, %d]", ErrInvalidInput, u.UnitID, u.VolumeML, maxUnitVolumeML)
		}
		group := st.groupKey(kind, u.Group)
		if kind.GroupApplicable && group == "" {
			return fmt.Errorf("%w: единица %s вида %s без группы", ErrInvalidInput, u.UnitID, u.Kind)
		}
		if err := checkGroup(kind, group, st.groups); err != nil {
			return fmt.Errorf("единица %s: %w", u.UnitID, err)
		}
		if u.Status != UnitStatusInStock || !u.ExpiresAt.After(st.now) {
			continue
		}
		c := st.cell(cellKey{clinic: u.ClinicID, kind: u.Kind, group: group})
		if !reserved[u.UnitID] {
			c.units = append(c.units, u)
		}
		c.availableML += u.VolumeML
		s := st.source(kindKey{clinic: u.ClinicID, kind: u.Kind})
		s.availableML += u.VolumeML
		s.availableUnits++
	}
	return nil
}
