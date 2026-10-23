// Package state — состояние имитатора АИСТ-М (C01) в памяти: единицы компонентов крови, их статусы
// и версии, закрепления за пациентом (ИНТ-05) и управляемые сбои. Имитатор не часть системы:
// базы нет, перезапуск теряет состояние. Время только через clock.Clock.
//
// Версия — глобальный счётчик: любое изменение единицы получает следующую версию, поэтому
// inventory может опрашивать изменения курсором `since` (ИНТ-01) и не пропустит ни одного.
// Выбывшая единица не удаляется и сохраняет исходный код АИСТ-М: прослеживаемость по ПП РФ № 641.
package state

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
)

// Статусы единицы в АИСТ-М (те же, что в libs/testdata).
const (
	StatusInStock     = "IN_STOCK"
	StatusQuarantine  = "QUARANTINE"
	StatusIssued      = "ISSUED"
	StatusDiscarded   = "DISCARDED"
	StatusTransferred = "TRANSFERRED"
)

// Причины выбытия.
const (
	ReasonIssued      = "ISSUED"
	ReasonDiscarded   = "DISCARDED"
	ReasonTransferred = "TRANSFERRED"
	ReasonExpired     = "EXPIRED"
)

// Ошибки состояния; HTTP-слой переводит их в коды ответа.
var (
	ErrNotFound            = errors.New("единица не найдена")
	ErrAlreadyExists       = errors.New("единица с таким кодом уже есть")
	ErrInvalid             = errors.New("некорректные данные")
	ErrWithdrawn           = errors.New("единица выбыла")
	ErrQuarantine          = errors.New("единица в карантине")
	ErrUnitUnavailable     = errors.New("единица недоступна для закрепления")
	ErrUnitTaken           = errors.New("единица уже закреплена за другим случаем")
	ErrIdempotencyMismatch = errors.New("ключ идемпотентности уже использован с другим содержимым")
)

// reasonStatus — в какой статус переводит причина выбытия. Истёкший срок годности в АИСТ-М — списание.
var reasonStatus = map[string]string{
	ReasonIssued:      StatusIssued,
	ReasonDiscarded:   StatusDiscarded,
	ReasonTransferred: StatusTransferred,
	ReasonExpired:     StatusDiscarded,
}

// Unit — единица компонента крови в терминах АИСТ-М.
type Unit struct {
	AistUnitCode     string    `json:"aist_unit_code"`
	ClinicCode       string    `json:"clinic_code"`
	ComponentKind    string    `json:"component_kind"`
	BloodGroup       string    `json:"blood_group"`
	Phenotype        string    `json:"phenotype,omitempty"`
	VolumeML         int64     `json:"volume_ml"`
	ExpiresAt        time.Time `json:"expires_at"`
	StorageLocation  string    `json:"storage_location"`
	Status           string    `json:"status"`
	WithdrawalReason string    `json:"withdrawal_reason,omitempty"`
	Version          int64     `json:"version"`
	ChangedAt        time.Time `json:"changed_at"`
}

// NewUnit — данные для создания единицы. Пустые поля получают значения по умолчанию.
type NewUnit struct {
	AistUnitCode    string
	ClinicCode      string
	ComponentKind   string
	BloodGroup      string
	Phenotype       string
	VolumeML        int64
	ExpiresAt       time.Time
	StorageLocation string
	Status          string
}

// AllocationRequest — закрепление единиц за пациентом (ИНТ-05).
type AllocationRequest struct {
	IdempotencyKey      string
	PatientCaseNo       string
	RecipientClinicCode string
	AistUnitCodes       []string
}

// Allocation — принятое закрепление. Номер случая хранится, но наружу и в логи не отдаётся.
type Allocation struct {
	AllocationID        string    `json:"allocation_id"`
	IdempotencyKey      string    `json:"idempotency_key"`
	PatientCaseNo       string    `json:"-"`
	RecipientClinicCode string    `json:"recipient_clinic_code"`
	AistUnitCodes       []string  `json:"aist_unit_codes"`
	AcknowledgedAt      time.Time `json:"acknowledged_at"`
}

// Faults — управляемые сбои: недоступность на время и задержка ответа.
type Faults struct {
	UnavailableFor time.Duration
	Slow           time.Duration
}

// Store — состояние имитатора. Безопасен для параллельных запросов.
type Store struct {
	clk clock.Clock
	ref *refdata.Set

	mu      sync.Mutex
	version int64
	units   map[string]*Unit
	// byVersion[v-1] — код единицы, получившей версию v. Запись устарела, если версия единицы уже больше.
	byVersion   []string
	allocations []*Allocation
	byKey       map[string]*Allocation
	allocatedTo map[string]string // код единицы → ключ закрепления
	generated   int64

	unavailableUntil time.Time
	slow             time.Duration
}

// New создаёт пустое состояние. Справочники нужны для проверки клиник, видов и групп.
func New(clk clock.Clock, ref *refdata.Set) *Store {
	return &Store{
		clk:         clk,
		ref:         ref,
		units:       map[string]*Unit{},
		byKey:       map[string]*Allocation{},
		allocatedTo: map[string]string{},
	}
}

// Version — последняя выданная версия.
func (s *Store) Version() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

// Create добавляет единицу. Начальный статус — IN_STOCK или QUARANTINE.
func (s *Store) Create(nu NewUnit) (Unit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.build(nu)
	if err != nil {
		return Unit{}, err
	}
	if _, ok := s.units[u.AistUnitCode]; ok {
		return Unit{}, fmt.Errorf("%w: %s", ErrAlreadyExists, u.AistUnitCode)
	}
	s.units[u.AistUnitCode] = u
	s.bump(u)
	return *u, nil
}

func (s *Store) build(nu NewUnit) (*Unit, error) {
	if !s.clinicExists(nu.ClinicCode) {
		return nil, fmt.Errorf("%w: неизвестная клиника %q", ErrInvalid, nu.ClinicCode)
	}
	kind, ok := s.ref.Kind(nu.ComponentKind)
	if !ok {
		return nil, fmt.Errorf("%w: неизвестный вид компонента %q", ErrInvalid, nu.ComponentKind)
	}
	if !s.groupExists(nu.BloodGroup) {
		return nil, fmt.Errorf("%w: неизвестная группа крови %q", ErrInvalid, nu.BloodGroup)
	}
	if nu.VolumeML < 0 {
		return nil, fmt.Errorf("%w: отрицательный объём", ErrInvalid)
	}
	status := nu.Status
	if status == "" {
		status = StatusInStock
	}
	if status != StatusInStock && status != StatusQuarantine {
		return nil, fmt.Errorf("%w: новая единица бывает только IN_STOCK или QUARANTINE, получено %q", ErrInvalid, status)
	}
	now := s.clk.Now()
	u := &Unit{
		AistUnitCode:    nu.AistUnitCode,
		ClinicCode:      nu.ClinicCode,
		ComponentKind:   kind.Code,
		BloodGroup:      nu.BloodGroup,
		Phenotype:       nu.Phenotype,
		VolumeML:        nu.VolumeML,
		ExpiresAt:       nu.ExpiresAt.UTC(),
		StorageLocation: nu.StorageLocation,
		Status:          status,
	}
	if u.AistUnitCode == "" {
		s.generated++
		u.AistUnitCode = fmt.Sprintf("AIST-SIM-%07d", s.generated)
	}
	if u.VolumeML == 0 {
		u.VolumeML = kind.TypicalVolumeML
	}
	if u.ExpiresAt.IsZero() {
		u.ExpiresAt = now.Add(time.Duration(kind.ShelfLifeDays) * 24 * time.Hour)
	}
	return u, nil
}

func (s *Store) clinicExists(code string) bool {
	for _, c := range s.ref.Clinics {
		if c.ClinicCode == code {
			return true
		}
	}
	return false
}

func (s *Store) groupExists(code string) bool {
	for _, g := range s.ref.BloodGroups {
		if g.Code == code {
			return true
		}
	}
	return false
}

// bump выдаёт единице следующую версию и отмечает время изменения. Вызывается под s.mu.
func (s *Store) bump(u *Unit) {
	s.version++
	u.Version = s.version
	u.ChangedAt = s.clk.Now()
	s.byVersion = append(s.byVersion, u.AistUnitCode)
}

// Withdraw фиксирует выбытие единицы. Единица в карантине может быть только списана.
func (s *Store) Withdraw(code, reason string) (Unit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.withdraw(code, reason)
}

func (s *Store) withdraw(code, reason string) (Unit, error) {
	status, ok := reasonStatus[reason]
	if !ok {
		return Unit{}, fmt.Errorf("%w: неизвестная причина выбытия %q", ErrInvalid, reason)
	}
	u, ok := s.units[code]
	if !ok {
		return Unit{}, fmt.Errorf("%w: %s", ErrNotFound, code)
	}
	if isWithdrawn(u.Status) {
		return Unit{}, fmt.Errorf("%w: %s в статусе %s", ErrWithdrawn, code, u.Status)
	}
	// ПП РФ № 641, п. 62: компонент на карантинизации не выдаётся и не передаётся.
	if u.Status == StatusQuarantine && status != StatusDiscarded {
		return Unit{}, fmt.Errorf("%w: %s нельзя выдать или передать", ErrQuarantine, code)
	}
	u.Status = status
	u.WithdrawalReason = reason
	s.bump(u)
	return *u, nil
}

// SetStatus меняет статус единицы. Выбывшая единица статус не меняет; перевод в статус выбытия
// идёт по правилам Withdraw. Тот же статус — без новой версии.
func (s *Store) SetStatus(code, status string) (Unit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch status {
	case StatusIssued, StatusDiscarded, StatusTransferred:
		u, ok := s.units[code]
		if ok && u.Status == status {
			return *u, nil
		}
		return s.withdraw(code, status)
	case StatusInStock, StatusQuarantine:
	default:
		return Unit{}, fmt.Errorf("%w: неизвестный статус %q", ErrInvalid, status)
	}
	u, ok := s.units[code]
	if !ok {
		return Unit{}, fmt.Errorf("%w: %s", ErrNotFound, code)
	}
	if u.Status == status {
		return *u, nil
	}
	if isWithdrawn(u.Status) {
		return Unit{}, fmt.Errorf("%w: %s в статусе %s", ErrWithdrawn, code, u.Status)
	}
	u.Status = status
	s.bump(u)
	return *u, nil
}

func isWithdrawn(status string) bool {
	return status == StatusIssued || status == StatusDiscarded || status == StatusTransferred
}

// Changes возвращает единицы с версией больше since по возрастанию версии, не больше limit.
// more = true, если за пределами лимита есть ещё изменения.
func (s *Store) Changes(since int64, limit int) (units []Unit, more bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if since < 0 {
		since = 0
	}
	units = []Unit{}
	for v := since + 1; v <= s.version; v++ {
		u := s.units[s.byVersion[v-1]]
		if u.Version != v {
			continue // единица с тех пор изменилась, её свежая версия встретится дальше
		}
		if len(units) == limit {
			return units, true
		}
		units = append(units, *u)
	}
	return units, false
}

// Statuses возвращает найденные единицы в порядке запроса и коды, которых нет.
func (s *Store) Statuses(codes []string) (found []Unit, missing []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found, missing = []Unit{}, []string{}
	for _, c := range codes {
		if u, ok := s.units[c]; ok {
			found = append(found, *u)
		} else {
			missing = append(missing, c)
		}
	}
	return found, missing
}

// Snapshot — все единицы, включая выбывшие, по коду.
func (s *Store) Snapshot() []Unit {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Unit, 0, len(s.units))
	for _, u := range s.units {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AistUnitCode < out[j].AistUnitCode })
	return out
}

// Seed добавляет сгенерированный набор. Единицы с уже известным кодом пропускаются: счётчик версий
// никогда не откатывается, иначе inventory пропустил бы изменения по курсору.
func (s *Store) Seed(units []testdata.Unit) (created, skipped int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tu := range units {
		if _, ok := s.units[tu.AistUnitCode]; ok {
			skipped++
			continue
		}
		status := tu.Status
		if status == "" || isWithdrawn(status) {
			status = StatusInStock
		}
		u, err := s.build(NewUnit{
			AistUnitCode: tu.AistUnitCode, ClinicCode: tu.ClinicCode, ComponentKind: tu.ComponentKind,
			BloodGroup: tu.BloodGroup, Phenotype: tu.Phenotype, VolumeML: tu.VolumeML,
			ExpiresAt: tu.ExpiresAt, StorageLocation: tu.StorageLocation, Status: status,
		})
		if err != nil {
			return created, skipped, fmt.Errorf("единица %s: %w", tu.AistUnitCode, err)
		}
		// Выбывшие в наборе единицы сразу в конечном статусе, причина совпадает со статусом.
		if isWithdrawn(tu.Status) {
			u.Status = tu.Status
			u.WithdrawalReason = tu.Status
		}
		s.units[u.AistUnitCode] = u
		s.bump(u)
		created++
	}
	return created, skipped, nil
}

// Allocate принимает закрепление единиц за пациентом. Повтор с тем же ключом и тем же содержимым
// возвращает исходный ответ и не создаёт второго закрепления (created = false).
func (s *Store) Allocate(req AllocationRequest) (a Allocation, created bool, err error) {
	if req.IdempotencyKey == "" {
		return Allocation{}, false, fmt.Errorf("%w: нет ключа идемпотентности", ErrInvalid)
	}
	if req.PatientCaseNo == "" {
		return Allocation{}, false, fmt.Errorf("%w: нет номера случая", ErrInvalid)
	}
	if len(req.AistUnitCodes) == 0 {
		return Allocation{}, false, fmt.Errorf("%w: пустой список единиц", ErrInvalid)
	}
	codes := slices.Clone(req.AistUnitCodes)
	slices.Sort(codes)
	codes = slices.Compact(codes)

	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.byKey[req.IdempotencyKey]; ok {
		if prev.PatientCaseNo != req.PatientCaseNo || prev.RecipientClinicCode != req.RecipientClinicCode ||
			!slices.Equal(prev.AistUnitCodes, codes) {
			return Allocation{}, false, ErrIdempotencyMismatch
		}
		return clone(prev), false, nil
	}
	if !s.clinicExists(req.RecipientClinicCode) {
		return Allocation{}, false, fmt.Errorf("%w: неизвестная клиника-получатель %q", ErrInvalid, req.RecipientClinicCode)
	}
	for _, c := range codes {
		u, ok := s.units[c]
		if !ok {
			return Allocation{}, false, fmt.Errorf("%w: %s", ErrNotFound, c)
		}
		if u.Status != StatusInStock {
			return Allocation{}, false, fmt.Errorf("%w: %s в статусе %s", ErrUnitUnavailable, c, u.Status)
		}
		if _, taken := s.allocatedTo[c]; taken {
			return Allocation{}, false, fmt.Errorf("%w: %s", ErrUnitTaken, c)
		}
	}
	na := &Allocation{
		AllocationID:        fmt.Sprintf("ALLOC-%07d", len(s.allocations)+1),
		IdempotencyKey:      req.IdempotencyKey,
		PatientCaseNo:       req.PatientCaseNo,
		RecipientClinicCode: req.RecipientClinicCode,
		AistUnitCodes:       codes,
		AcknowledgedAt:      s.clk.Now(),
	}
	s.allocations = append(s.allocations, na)
	s.byKey[na.IdempotencyKey] = na
	for _, c := range codes {
		s.allocatedTo[c] = na.IdempotencyKey
	}
	return clone(na), true, nil
}

// Allocations — принятые закрепления в порядке приёма.
func (s *Store) Allocations() []Allocation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Allocation, 0, len(s.allocations))
	for _, a := range s.allocations {
		out = append(out, clone(a))
	}
	return out
}

func clone(a *Allocation) Allocation {
	c := *a
	c.AistUnitCodes = slices.Clone(a.AistUnitCodes)
	return c
}

// SetFaults задаёт сбои, отсчёт недоступности — от текущего времени. Нулевые значения снимают сбой.
func (s *Store) SetFaults(f Faults) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unavailableUntil = time.Time{}
	if f.UnavailableFor > 0 {
		s.unavailableUntil = s.clk.Now().Add(f.UnavailableFor)
	}
	s.slow = max(f.Slow, 0)
}

// Unavailable сообщает, действует ли сбой недоступности, и сколько ему осталось.
func (s *Store) Unavailable() (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailableUntil.IsZero() {
		return false, 0
	}
	left := s.unavailableUntil.Sub(s.clk.Now())
	if left <= 0 {
		return false, 0
	}
	return true, left
}

// Slow — текущая задержка ответа.
func (s *Store) Slow() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.slow
}
