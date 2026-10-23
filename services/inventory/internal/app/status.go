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
	// SourceAist — статус получен из АИСТ-М в этом запросе.
	SourceAist = "aist"
	// SourceCopy — АИСТ-М не ответила, статус из копии inventory; актуальность — водяной знак.
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

// UnitStatuses — точечный статус единиц (ADR-0004, FR048). Сначала АИСТ-М с бюджетом StatusTimeout,
// при её недоступности — копия с source=copy и водяным знаком: решение принять такой ответ принимает transfer.
// Копия при этом не меняется: её обновляет только опрос, который публикует события.
func (s *Service) UnitStatuses(ctx context.Context, ids []uuid.UUID) (StatusReport, error) {
	ids = dedupe(ids)
	if len(ids) == 0 || len(ids) > s.cfg.MaxStatusIDs {
		return StatusReport{}, fmt.Errorf("%w: нужно от 1 до %d идентификаторов единиц", ErrInvalid, s.cfg.MaxStatusIDs)
	}
	units, err := s.store.UnitsByIDs(ctx, ids)
	if err != nil {
		return StatusReport{}, err
	}
	watermark, err := s.store.Watermark(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	byID := make(map[uuid.UUID]domain.Unit, len(units))
	for _, u := range units {
		byID[u.UnitID] = u
	}
	var notFound []uuid.UUID
	for _, id := range ids {
		if _, ok := byID[id]; !ok {
			notFound = append(notFound, id)
		}
	}
	if len(units) == 0 {
		return s.fromCopy(nil, notFound, watermark), nil
	}

	codes := make([]string, 0, len(units))
	byCode := make(map[string]domain.Unit, len(units))
	for _, u := range units {
		codes = append(codes, u.AistUnitCode)
		byCode[u.AistUnitCode] = u
	}
	actx, cancel := context.WithTimeout(ctx, s.cfg.StatusTimeout)
	defer cancel()
	page, err := s.aist.Statuses(actx, codes)
	if err == nil {
		err = validStatuses(page)
	}
	if err != nil {
		s.log.WarnContext(ctx, "АИСТ-М не ответила на запрос статуса, ответ из копии", "units", len(units), "err", err)
		return s.fromCopy(units, notFound, watermark), nil
	}

	rep := StatusReport{Source: SourceAist, CheckedAt: page.CheckedAt, Watermark: watermark}
	if rep.CheckedAt.IsZero() {
		rep.CheckedAt = s.clock.Now()
	}
	for _, st := range page.Units {
		u, ok := byCode[st.AistUnitCode]
		if !ok {
			continue // источник вернул код, о котором не спрашивали
		}
		rep.Units = append(rep.Units, UnitStatus{
			UnitID: u.UnitID, ClinicID: u.ClinicID, ComponentKind: u.ComponentKind, Status: st.Status,
			Available: s.ref.IsUnitAvailable(st.Status), Version: st.Version, ChangedAt: st.ChangedAt,
			ExpiresAt: u.ExpiresAt,
		})
	}
	for _, code := range page.NotFound {
		if u, ok := byCode[code]; ok {
			rep.NotFound = append(rep.NotFound, u.UnitID)
		}
	}
	rep.NotFound = append(rep.NotFound, notFound...)
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
			return fmt.Errorf("%w: неизвестный статус %q", domain.ErrInvalidSource, st.Status)
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
