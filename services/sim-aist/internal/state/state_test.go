package state_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-aist/internal/state"
)

var t0 = time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)

func newStore(t *testing.T) (*state.Store, *clock.Mutable, *refdata.Set) {
	t.Helper()
	ref := refdata.MustLoad()
	clk := clock.NewMutable(t0)
	return state.New(clk, ref), clk, ref
}

func newUnit(ref *refdata.Set, code string) state.NewUnit {
	return state.NewUnit{
		AistUnitCode:  code,
		ClinicCode:    ref.Clinics[0].ClinicCode,
		ComponentKind: "RBC",
		BloodGroup:    "O+",
		VolumeML:      300,
		ExpiresAt:     t0.Add(30 * 24 * time.Hour),
	}
}

func mustCreate(t *testing.T, s *state.Store, nu state.NewUnit) state.Unit {
	t.Helper()
	u, err := s.Create(nu)
	if err != nil {
		t.Fatalf("создание %s: %v", nu.AistUnitCode, err)
	}
	return u
}

func TestCreateAssignsMonotonicVersions(t *testing.T) {
	s, _, ref := newStore(t)
	a := mustCreate(t, s, newUnit(ref, "U-1"))
	b := mustCreate(t, s, newUnit(ref, "U-2"))
	if a.Status != state.StatusInStock {
		t.Fatalf("статус по умолчанию: %s", a.Status)
	}
	if a.Version <= 0 || b.Version <= a.Version {
		t.Fatalf("версии не растут: %d, %d", a.Version, b.Version)
	}
	if !a.ChangedAt.Equal(t0) {
		t.Fatalf("время изменения не из Clock: %s", a.ChangedAt)
	}
}

func TestCreateRejectsDuplicateAndUnknownReference(t *testing.T) {
	s, _, ref := newStore(t)
	mustCreate(t, s, newUnit(ref, "U-1"))
	if _, err := s.Create(newUnit(ref, "U-1")); !errors.Is(err, state.ErrAlreadyExists) {
		t.Fatalf("дубль кода: ожидали ErrAlreadyExists, получили %v", err)
	}
	bad := newUnit(ref, "U-2")
	bad.ClinicCode = "NO-SUCH-CLINIC"
	if _, err := s.Create(bad); !errors.Is(err, state.ErrInvalid) {
		t.Fatalf("неизвестная клиника: ожидали ErrInvalid, получили %v", err)
	}
	bad = newUnit(ref, "U-3")
	bad.ComponentKind = "XXX"
	if _, err := s.Create(bad); !errors.Is(err, state.ErrInvalid) {
		t.Fatalf("неизвестный вид: ожидали ErrInvalid, получили %v", err)
	}
}

func TestWithdrawChangesStatusAndVersionAndKeepsUnit(t *testing.T) {
	s, clk, ref := newStore(t)
	u := mustCreate(t, s, newUnit(ref, "U-1"))
	clk.Advance(time.Minute)
	w, err := s.Withdraw("U-1", state.ReasonIssued)
	if err != nil {
		t.Fatalf("выбытие: %v", err)
	}
	if w.Status != state.StatusIssued || w.WithdrawalReason != state.ReasonIssued {
		t.Fatalf("после выдачи: статус %s, причина %s", w.Status, w.WithdrawalReason)
	}
	if w.Version <= u.Version {
		t.Fatalf("версия не выросла: было %d, стало %d", u.Version, w.Version)
	}
	if !w.ChangedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("время изменения: %s", w.ChangedAt)
	}
	found, missing := s.Statuses([]string{"U-1"})
	if len(missing) != 0 || len(found) != 1 || found[0].AistUnitCode != "U-1" {
		t.Fatalf("выбывшая единица должна оставаться в состоянии: %+v, %v", found, missing)
	}
	if _, err := s.Withdraw("U-1", state.ReasonDiscarded); !errors.Is(err, state.ErrWithdrawn) {
		t.Fatalf("повторное выбытие: ожидали ErrWithdrawn, получили %v", err)
	}
}

func TestWithdrawReasons(t *testing.T) {
	cases := map[string]string{
		state.ReasonIssued:      state.StatusIssued,
		state.ReasonDiscarded:   state.StatusDiscarded,
		state.ReasonTransferred: state.StatusTransferred,
		state.ReasonExpired:     state.StatusDiscarded,
	}
	for reason, want := range cases {
		t.Run(reason, func(t *testing.T) {
			s, _, ref := newStore(t)
			mustCreate(t, s, newUnit(ref, "U-1"))
			u, err := s.Withdraw("U-1", reason)
			if err != nil {
				t.Fatalf("выбытие: %v", err)
			}
			if u.Status != want || u.WithdrawalReason != reason {
				t.Fatalf("причина %s: статус %s, причина %s", reason, u.Status, u.WithdrawalReason)
			}
		})
	}
	s, _, ref := newStore(t)
	mustCreate(t, s, newUnit(ref, "U-1"))
	if _, err := s.Withdraw("U-1", "LOST"); !errors.Is(err, state.ErrInvalid) {
		t.Fatalf("неизвестная причина: ожидали ErrInvalid, получили %v", err)
	}
	if _, err := s.Withdraw("U-404", state.ReasonIssued); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("нет единицы: ожидали ErrNotFound, получили %v", err)
	}
}

func TestQuarantineRoundTripAndNoIssueFromQuarantine(t *testing.T) {
	s, _, ref := newStore(t)
	mustCreate(t, s, newUnit(ref, "U-1"))
	q, err := s.SetStatus("U-1", state.StatusQuarantine)
	if err != nil {
		t.Fatalf("в карантин: %v", err)
	}
	found, _ := s.Statuses([]string{"U-1"})
	if found[0].Status != state.StatusQuarantine {
		t.Fatalf("статус в выборке: %s", found[0].Status)
	}
	if _, err := s.Withdraw("U-1", state.ReasonIssued); !errors.Is(err, state.ErrQuarantine) {
		t.Fatalf("выдача из карантина: ожидали ErrQuarantine, получили %v", err)
	}
	back, err := s.SetStatus("U-1", state.StatusInStock)
	if err != nil {
		t.Fatalf("из карантина: %v", err)
	}
	if back.Status != state.StatusInStock || back.Version <= q.Version {
		t.Fatalf("возврат из карантина: статус %s, версия %d после %d", back.Status, back.Version, q.Version)
	}
	same, err := s.SetStatus("U-1", state.StatusInStock)
	if err != nil || same.Version != back.Version {
		t.Fatalf("тот же статус не должен давать новую версию: %d → %d, %v", back.Version, same.Version, err)
	}
}

func TestSetStatusOnWithdrawnUnitIsRejected(t *testing.T) {
	s, _, ref := newStore(t)
	mustCreate(t, s, newUnit(ref, "U-1"))
	if _, err := s.SetStatus("U-1", state.StatusDiscarded); err != nil {
		t.Fatalf("списание через смену статуса: %v", err)
	}
	if _, err := s.SetStatus("U-1", state.StatusInStock); !errors.Is(err, state.ErrWithdrawn) {
		t.Fatalf("оживление выбывшей: ожидали ErrWithdrawn, получили %v", err)
	}
	if _, err := s.SetStatus("U-1", "BROKEN"); !errors.Is(err, state.ErrInvalid) {
		t.Fatalf("неизвестный статус: ожидали ErrInvalid, получили %v", err)
	}
}

func TestChangesReturnsOnlyNewerVersionsInOrder(t *testing.T) {
	s, _, ref := newStore(t)
	mustCreate(t, s, newUnit(ref, "U-1"))
	u2 := mustCreate(t, s, newUnit(ref, "U-2"))
	mustCreate(t, s, newUnit(ref, "U-3"))
	if _, err := s.Withdraw("U-1", state.ReasonIssued); err != nil {
		t.Fatal(err)
	}
	got, more := s.Changes(u2.Version, 100)
	if more {
		t.Fatal("лишний признак продолжения")
	}
	codes := make([]string, 0, len(got))
	for i, u := range got {
		if u.Version <= u2.Version {
			t.Fatalf("старая версия в выдаче: %+v", u)
		}
		if i > 0 && u.Version <= got[i-1].Version {
			t.Fatalf("версии не по возрастанию: %d после %d", u.Version, got[i-1].Version)
		}
		codes = append(codes, u.AistUnitCode)
	}
	if len(codes) != 2 || codes[0] != "U-3" || codes[1] != "U-1" {
		t.Fatalf("ожидали U-3, U-1, получили %v", codes)
	}
	page, more := s.Changes(0, 2)
	if len(page) != 2 || !more {
		t.Fatalf("лимит: %d единиц, продолжение %v", len(page), more)
	}
	if rest, _ := s.Changes(s.Version(), 10); len(rest) != 0 {
		t.Fatalf("после последней версии изменений нет, получили %d", len(rest))
	}
}

func TestChangesSinceBeyondCurrentVersionIsEmpty(t *testing.T) {
	s, _, ref := newStore(t)
	if got, more := s.Changes(math.MaxInt64, 10); len(got) != 0 || more {
		t.Fatalf("пустое состояние, since=MaxInt64: %d единиц, продолжение %v", len(got), more)
	}
	mustCreate(t, s, newUnit(ref, "U-1"))
	mustCreate(t, s, newUnit(ref, "U-2"))
	for _, since := range []int64{s.Version(), s.Version() + 1, 1 << 40, math.MaxInt64} {
		if got, more := s.Changes(since, 10); len(got) != 0 || more {
			t.Fatalf("since=%d: %d единиц, продолжение %v", since, len(got), more)
		}
	}
	// Отрицательный since равен нулю: все единицы.
	if got, _ := s.Changes(-5, 10); len(got) != 2 {
		t.Fatalf("since=-5: %d единиц, ожидали 2", len(got))
	}
}

func TestStatusesReportsMissing(t *testing.T) {
	s, _, ref := newStore(t)
	mustCreate(t, s, newUnit(ref, "U-1"))
	found, missing := s.Statuses([]string{"U-1", "U-404"})
	if len(found) != 1 || len(missing) != 1 || missing[0] != "U-404" {
		t.Fatalf("найдено %+v, нет %v", found, missing)
	}
}

func TestAllocateIsIdempotentByKey(t *testing.T) {
	s, _, ref := newStore(t)
	mustCreate(t, s, newUnit(ref, "U-1"))
	mustCreate(t, s, newUnit(ref, "U-2"))
	req := state.AllocationRequest{
		IdempotencyKey: "k-1", PatientCaseNo: "CASE-1",
		RecipientClinicCode: ref.Clinics[1].ClinicCode, AistUnitCodes: []string{"U-1", "U-2"},
	}
	a, created, err := s.Allocate(req)
	if err != nil || !created {
		t.Fatalf("первое закрепление: created=%v, %v", created, err)
	}
	b, created, err := s.Allocate(req)
	if err != nil || created {
		t.Fatalf("повтор: created=%v, %v", created, err)
	}
	if a.AllocationID != b.AllocationID || !a.AcknowledgedAt.Equal(b.AcknowledgedAt) {
		t.Fatalf("повтор дал другой ответ: %+v и %+v", a, b)
	}
	if n := len(s.Allocations()); n != 1 {
		t.Fatalf("закреплений %d, ожидали 1", n)
	}
	other := req
	other.AistUnitCodes = []string{"U-1"}
	if _, _, err := s.Allocate(other); !errors.Is(err, state.ErrIdempotencyMismatch) {
		t.Fatalf("тот же ключ с другим содержимым: ожидали ErrIdempotencyMismatch, получили %v", err)
	}
	second := req
	second.IdempotencyKey = "k-2"
	second.AistUnitCodes = []string{"U-2"}
	if _, _, err := s.Allocate(second); !errors.Is(err, state.ErrUnitTaken) {
		t.Fatalf("единица уже закреплена: ожидали ErrUnitTaken, получили %v", err)
	}
}

func TestAllocateRequiresUnitsInStock(t *testing.T) {
	s, _, ref := newStore(t)
	mustCreate(t, s, newUnit(ref, "U-1"))
	if _, err := s.SetStatus("U-1", state.StatusQuarantine); err != nil {
		t.Fatal(err)
	}
	req := state.AllocationRequest{
		IdempotencyKey: "k-1", PatientCaseNo: "CASE-1",
		RecipientClinicCode: ref.Clinics[0].ClinicCode, AistUnitCodes: []string{"U-1"},
	}
	if _, _, err := s.Allocate(req); !errors.Is(err, state.ErrUnitUnavailable) {
		t.Fatalf("единица в карантине: ожидали ErrUnitUnavailable, получили %v", err)
	}
	req.AistUnitCodes = []string{"U-404"}
	if _, _, err := s.Allocate(req); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("нет единицы: ожидали ErrNotFound, получили %v", err)
	}
	req.AistUnitCodes = nil
	if _, _, err := s.Allocate(req); !errors.Is(err, state.ErrInvalid) {
		t.Fatalf("пустой список: ожидали ErrInvalid, получили %v", err)
	}
}

func TestSeedAddsDatasetAndSkipsExisting(t *testing.T) {
	s, _, ref := newStore(t)
	ds := testdata.Generate(ref, testdata.Config{Seed: 7, Units: 50, Now: t0, WithdrawnShare: 0.2})
	created, skipped, err := s.Seed(ds.Units)
	if err != nil || created != 50 || skipped != 0 {
		t.Fatalf("первый сид: создано %d, пропущено %d, %v", created, skipped, err)
	}
	v := s.Version()
	created, skipped, err = s.Seed(ds.Units)
	if err != nil || created != 0 || skipped != 50 {
		t.Fatalf("повторный сид: создано %d, пропущено %d, %v", created, skipped, err)
	}
	if s.Version() != v {
		t.Fatalf("повторный сид не должен менять версию: %d → %d", v, s.Version())
	}
	for _, u := range s.Snapshot() {
		withdrawn := u.Status == state.StatusIssued || u.Status == state.StatusDiscarded || u.Status == state.StatusTransferred
		if withdrawn != (u.WithdrawalReason != "") {
			t.Fatalf("причина выбытия не согласована со статусом: %+v", u)
		}
	}
}

func TestFaultsUnavailableForDuration(t *testing.T) {
	s, clk, _ := newStore(t)
	if down, _ := s.Unavailable(); down {
		t.Fatal("по умолчанию имитатор доступен")
	}
	s.SetFaults(state.Faults{UnavailableFor: 30 * time.Second, Slow: 50 * time.Millisecond})
	down, retry := s.Unavailable()
	if !down || retry != 30*time.Second {
		t.Fatalf("сразу после сбоя: недоступен=%v, повтор через %s", down, retry)
	}
	if s.Slow() != 50*time.Millisecond {
		t.Fatalf("задержка: %s", s.Slow())
	}
	clk.Advance(29 * time.Second)
	if down, _ := s.Unavailable(); !down {
		t.Fatal("за секунду до конца сбоя имитатор ещё недоступен")
	}
	clk.Advance(time.Second)
	if down, _ := s.Unavailable(); down {
		t.Fatal("по истечении срока имитатор доступен")
	}
}
