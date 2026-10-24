package app

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

// Источник ответа о статусе единиц.
const (
	// SourceAist — статус получен из АИСТ-М в этом запросе (или спрашивать было не о чем: ни одной единицы нет в копии).
	SourceAist = "aist"
	// SourceCopy — АИСТ-М не ответила (ошибка, таймаут, предохранитель), статус из копии inventory;
	// актуальность — водяной знак. transfer трактует это как недоступность источника.
	SourceCopy = "copy"
)

// UnitStatus — статус единицы для transfer. Available — по справочнику ref_unit_status (только IN_STOCK;
// единица на карантине недоступна, ПП РФ № 641, п. 62).
type UnitStatus struct {
	UnitID        uuid.UUID
	ClinicID      int64
	ComponentKind string
	Status        string
	Available     bool
	Version       int64
	ChangedAt     time.Time
	ExpiresAt     time.Time
}

// StatusReport — ответ GET /internal/units/status.
type StatusReport struct {
	Source    string
	CheckedAt time.Time
	// Watermark — окончание последнего успешного опроса АИСТ-М; nil, если опроса ещё не было.
	Watermark *time.Time
	Units     []UnitStatus
	NotFound  []uuid.UUID
}

// statusBudget — сколько можно ждать АИСТ-М: не больше StatusTimeout и не позже дедлайна входящего
// запроса минус резерв на ответ из копии. Дедлайн контекста — время процесса, поэтому здесь time.Until,
// а не Clock: Clock задаёт бизнес-время, а не таймауты сети.
func (s *Service) statusBudget(ctx context.Context) time.Duration {
	budget := s.cfg.StatusTimeout
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl) - s.cfg.StatusReserve; left < budget {
			budget = left
		}
	}
	return budget
}

// UnitStatuses — точечный статус единиц (ADR-0004, FR048). Сначала копия (короткий таймаут БД), затем АИСТ-М
// в пределах бюджета; если АИСТ-М не ответила вовремя — ответ из копии с source=copy и водяным знаком.
// Копия при этом не меняется: её обновляет только опрос, который публикует события.
func (s *Service) UnitStatuses(ctx context.Context, ids []uuid.UUID) (StatusReport, error) {
	ids = dedupe(ids)
	if len(ids) == 0 || len(ids) > s.cfg.MaxStatusIDs {
		return StatusReport{}, fmt.Errorf("%w: нужно от 1 до %d идентификаторов единиц", ErrInvalid, s.cfg.MaxStatusIDs)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.cfg.StatusBudget)
		defer cancel()
	}

	dbctx, cancel := context.WithTimeout(ctx, s.cfg.DBTimeout)
	units, err := s.store.UnitsByIDs(dbctx, ids)
	var watermark *time.Time
	if err == nil {
		watermark, err = s.store.Watermark(dbctx)
	}
	cancel()
	if err != nil {
		return StatusReport{}, err
	}

	byID := make(map[uuid.UUID]domain.Unit, len(units))
	byCode := make(map[string]domain.Unit, len(units))
	codes := make([]string, 0, len(units))
	for _, u := range units {
		byID[u.UnitID] = u
		byCode[u.AistUnitCode] = u
		codes = append(codes, u.AistUnitCode)
	}
	var unknown []uuid.UUID
	for _, id := range ids {
		if _, ok := byID[id]; !ok {
			unknown = append(unknown, id)
		}
	}
	if len(units) == 0 {
		// Спрашивать АИСТ-М не о чем: это не её недоступность, поэтому не copy (иначе transfer
		// ошибочно эскалирует «источник недоступен»).
		return StatusReport{Source: SourceAist, CheckedAt: s.clock.Now(), Watermark: watermark, NotFound: unknown}, nil
	}

	budget := s.statusBudget(ctx)
	if budget <= 0 {
		s.log.WarnContext(ctx, "нет времени спросить АИСТ-М, ответ из копии", "units", len(units))
		return s.fromCopy(units, unknown, watermark), nil
	}
	actx, cancelAist := context.WithTimeout(ctx, budget)
	page, err := s.aist.Statuses(actx, codes)
	cancelAist()
	if err == nil {
		err = validStatuses(page)
	}
	if err != nil {
		// В лог — только вид отказа и число единиц: коды АИСТ-М не логируются (ADR-0010).
		s.log.WarnContext(ctx, "АИСТ-М не ответила на запрос статуса, ответ из копии",
			"units", len(units), "failure", FailureKind(err))
		return s.fromCopy(units, unknown, watermark), nil
	}

	rep := StatusReport{Source: SourceAist, CheckedAt: page.CheckedAt, Watermark: watermark}
	if rep.CheckedAt.IsZero() {
		rep.CheckedAt = s.clock.Now()
	}
	answered := make(map[string]bool, len(page.Units))
	for _, st := range page.Units {
		u, ok := byCode[st.AistUnitCode]
		if !ok || answered[st.AistUnitCode] {
			continue // источник вернул код, о котором не спрашивали, или повторил его
		}
		answered[st.AistUnitCode] = true
		rep.Units = append(rep.Units, UnitStatus{
			UnitID: u.UnitID, ClinicID: u.ClinicID, ComponentKind: u.ComponentKind, Status: st.Status,
			Available: s.ref.IsUnitAvailable(st.Status), Version: st.Version, ChangedAt: st.ChangedAt,
			ExpiresAt: u.ExpiresAt,
		})
	}
	// Единица, о которой АИСТ-М промолчала (нет ни в units, ни в not_found), считается ненайденной:
	// подтверждать её вслепую нельзя.
	for _, u := range units {
		if !answered[u.AistUnitCode] {
			rep.NotFound = append(rep.NotFound, u.UnitID)
		}
	}
	rep.NotFound = append(rep.NotFound, unknown...)
	return rep, nil
}

func (s *Service) fromCopy(units []domain.Unit, notFound []uuid.UUID, watermark *time.Time) StatusReport {
	rep := StatusReport{Source: SourceCopy, CheckedAt: s.clock.Now(), Watermark: watermark, NotFound: notFound}
	for _, u := range units {
		rep.Units = append(rep.Units, UnitStatus{
			UnitID: u.UnitID, ClinicID: u.ClinicID, ComponentKind: u.ComponentKind, Status: u.Status,
			Available: s.ref.IsUnitAvailable(u.Status), Version: u.Version, ChangedAt: u.ChangedAt,
			ExpiresAt: u.ExpiresAt,
		})
	}
	return rep
}

// validStatuses отбраковывает ответ с неизвестным статусом: такой ответ хуже честного ответа из копии.
func validStatuses(p StatusPage) error {
	for _, st := range p.Units {
		if !domain.KnownStatus(st.Status) {
			return &AistFailure{Kind: AistBadResponse, Operation: "/units/status"}
		}
	}
	return nil
}

func dedupe(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
