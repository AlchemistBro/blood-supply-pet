package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

// ErrNotFound — клиники или единицы нет.
var ErrNotFound = errors.New("не найдено")

// Ограничения страниц просмотра.
const (
	defaultUnitLimit = 100
	maxUnitLimit     = 1000
	defaultRunLimit  = 50
	maxRunLimit      = 500
)

// Usable — единица входит в фактический запас в момент at: статус доступен по справочнику ref_unit_status
// (карантин недоступен, ПП РФ № 641, п. 62 а) и срок годности не истёк (FR007).
func Usable(ref *refdata.Set, u domain.Unit, at time.Time) bool {
	return domain.Usable(ref.IsUnitAvailable(u.Status), u.ExpiresAt, at)
}

// Freshness — актуальность копии запаса для экрана (NFR015).
type Freshness struct {
	// AsOf — момент, на который посчитан ответ (часы сервиса).
	AsOf time.Time
	// Watermark — окончание последнего успешного опроса АИСТ-М; nil, если его не было.
	Watermark *time.Time
	// Stale — обновление старше порога StaleAfter: экран показывает предупреждение.
	Stale bool
}

// StockSummary — фактический запас клиники по виду и группе (O02, FR010, FR011).
type StockSummary struct {
	ClinicID int64
	Freshness
	Lines []StockLine
}

// UnitView — единица для просмотра: состояние копии, резус по справочнику групп и доступность на момент ответа.
// Исходный код АИСТ-М в просмотр не попадает (ADR-0010): обработчики его не отдают.
type UnitView struct {
	domain.Unit
	Available bool
	Expired   bool
}

// UnitPage — страница единиц фактического запаса.
type UnitPage struct {
	ClinicID int64
	Freshness
	Units   []UnitView
	HasMore bool
}

// SyncOverview — последний сеанс обмена и последний успешный (FR050, NFR015).
type SyncOverview struct {
	Freshness
	LastRun     *SyncRunView
	LastSuccess *SyncRunView
}

func (s *Service) freshness(ctx context.Context) (Freshness, error) {
	now := s.clock.Now()
	wm, err := s.store.Watermark(ctx)
	if err != nil {
		return Freshness{}, err
	}
	return Freshness{AsOf: now, Watermark: wm, Stale: domain.Stale(wm, now, s.cfg.StaleAfter)}, nil
}

// ClinicExists — клиника есть в справочнике.
func (s *Service) ClinicExists(id int64) bool {
	_, ok := s.ref.Clinic(id)
	return ok
}

// Stock — сводка фактического запаса клиники с водяным знаком (FR010, FR011, NFR015).
func (s *Service) Stock(ctx context.Context, clinicID int64) (StockSummary, error) {
	if !s.ClinicExists(clinicID) {
		return StockSummary{}, fmt.Errorf("%w: клиника %d", ErrNotFound, clinicID)
	}
	f, err := s.freshness(ctx)
	if err != nil {
		return StockSummary{}, err
	}
	lines, err := s.store.ActualStock(ctx, clinicID, f.AsOf)
	if err != nil {
		return StockSummary{}, err
	}
	return StockSummary{ClinicID: clinicID, Freshness: f, Lines: lines}, nil
}

// Units — единицы фактического запаса клиники: в наличии и не истёкшие на момент ответа, с фильтрами
// по виду и группе; при fefo — по ближайшей годности (FR015). Единицы, которые истекут до прибытия,
// здесь не исключаются: это решает подбор по времени доставки.
func (s *Service) Units(ctx context.Context, f UnitFilter) (UnitPage, error) {
	if !s.ClinicExists(f.ClinicID) {
		return UnitPage{}, fmt.Errorf("%w: клиника %d", ErrNotFound, f.ClinicID)
	}
	if f.ComponentKind != "" {
		if _, ok := s.ref.Kind(f.ComponentKind); !ok {
			return UnitPage{}, fmt.Errorf("%w: неизвестный вид компонента %q", ErrInvalid, f.ComponentKind)
		}
	}
	if f.BloodGroup != "" {
		if _, ok := s.cat.rhPositive[f.BloodGroup]; !ok {
			return UnitPage{}, fmt.Errorf("%w: неизвестная группа крови %q", ErrInvalid, f.BloodGroup)
		}
	}
	switch {
	case f.Limit == 0:
		f.Limit = defaultUnitLimit
	case f.Limit < 0 || f.Limit > maxUnitLimit:
		return UnitPage{}, fmt.Errorf("%w: limit от 1 до %d", ErrInvalid, maxUnitLimit)
	}
	if f.Offset < 0 {
		return UnitPage{}, fmt.Errorf("%w: offset не может быть отрицательным", ErrInvalid)
	}
	fr, err := s.freshness(ctx)
	if err != nil {
		return UnitPage{}, err
	}
	f.At = fr.AsOf
	want := f.Limit
	f.Limit++ // лишняя строка показывает, есть ли следующая страница
	units, err := s.store.AvailableUnits(ctx, f)
	if err != nil {
		return UnitPage{}, err
	}
	page := UnitPage{ClinicID: f.ClinicID, Freshness: fr}
	if len(units) > want {
		page.HasMore = true
		units = units[:want]
	}
	page.Units = make([]UnitView, 0, len(units))
	for _, u := range units {
		page.Units = append(page.Units, s.view(u, fr.AsOf))
	}
	return page, nil
}

func (s *Service) view(u domain.Unit, at time.Time) UnitView {
	u.RhPositive = s.cat.rhPositive[u.BloodGroup]
	return UnitView{Unit: u, Available: Usable(s.ref, u, at), Expired: !u.ExpiresAt.After(at)}
}

// Unit — карточка единицы (FR002–FR009) в любом статусе, в том числе выбывшей.
func (s *Service) Unit(ctx context.Context, id uuid.UUID) (UnitView, error) {
	u, err := s.store.GetUnit(ctx, id)
	if err != nil {
		return UnitView{}, err
	}
	if u == nil {
		return UnitView{}, fmt.Errorf("%w: единица %s", ErrNotFound, id)
	}
	return s.view(*u, s.clock.Now()), nil
}

// UnitHistory — история статусов единицы (FR008). Единица нужна вызывающему для проверки клиники.
func (s *Service) UnitHistory(ctx context.Context, id uuid.UUID) (UnitView, []StatusChange, error) {
	u, err := s.Unit(ctx, id)
	if err != nil {
		return UnitView{}, nil, err
	}
	h, err := s.store.UnitHistory(ctx, id)
	if err != nil {
		return UnitView{}, nil, err
	}
	return u, h, nil
}

// LastSync — последний сеанс опроса изменений и последний успешный (время и результат обновления, FR050).
func (s *Service) LastSync(ctx context.Context) (SyncOverview, error) {
	f, err := s.freshness(ctx)
	if err != nil {
		return SyncOverview{}, err
	}
	last, err := s.store.LastSyncRun(ctx, SyncKindChanges, false)
	if err != nil {
		return SyncOverview{}, err
	}
	ok, err := s.store.LastSyncRun(ctx, SyncKindChanges, true)
	if err != nil {
		return SyncOverview{}, err
	}
	return SyncOverview{Freshness: f, LastRun: last, LastSuccess: ok}, nil
}

// SyncKinds — виды сеансов обмена (sync_run.sync_kind).
var SyncKinds = []string{SyncKindChanges, "DAILY_SNAPSHOT", "ON_DEMAND", "UNIT_CHECK"}

// SyncRuns — журнал сеансов обмена, новые первыми; курсор — before (sync_run_id последней строки страницы).
func (s *Service) SyncRuns(ctx context.Context, f SyncRunFilter) ([]SyncRunView, error) {
	if f.Kind != "" {
		known := false
		for _, k := range SyncKinds {
			known = known || k == f.Kind
		}
		if !known {
			return nil, fmt.Errorf("%w: неизвестный вид сеанса %q", ErrInvalid, f.Kind)
		}
	}
	switch {
	case f.Limit == 0:
		f.Limit = defaultRunLimit
	case f.Limit < 0 || f.Limit > maxRunLimit:
		return nil, fmt.Errorf("%w: limit от 1 до %d", ErrInvalid, maxRunLimit)
	}
	return s.store.ListSyncRuns(ctx, f)
}
