package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

var (
	t0      = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	newID   = uuid.MustParse("01920000-0000-7000-8000-000000000001")
	knownID = uuid.MustParse("01920000-0000-7000-8000-0000000000aa")
)

func source(status, reason string, version int64) domain.SourceUnit {
	return domain.SourceUnit{
		AistUnitCode: "U-000001", ClinicID: 1, ComponentKind: "RBC", BloodGroup: "A+", RhPositive: true,
		VolumeML: 300, ExpiresAt: t0.Add(30 * 24 * time.Hour), StorageLocation: "холодильник 1",
		Status: status, WithdrawalReason: reason, Version: version, ChangedAt: t0,
	}
}

func stored(status string, version int64) *domain.Unit {
	u := domain.Unit{UnitID: knownID, SourceUnit: source(status, "", version), StatusReceivedAt: t0}
	return &u
}

func TestApplyMapsSourceChangesToEvents(t *testing.T) {
	cases := []struct {
		name          string
		prev          *domain.Unit
		in            domain.SourceUnit
		event         domain.EventKind
		reason        string
		statusChanged bool
	}{
		{"новая единица в наличии", nil, source(domain.StatusInStock, "", 1), domain.EventRegistered, "", true},
		{"новая единица на карантине", nil, source(domain.StatusQuarantine, "", 1), domain.EventRegistered, "", true},
		{"новая единица, уже выданная до первого опроса", nil, source(domain.StatusIssued, domain.ReasonIssued, 1), domain.EventRegistered, "", true},
		{"выдача", stored(domain.StatusInStock, 1), source(domain.StatusIssued, domain.ReasonIssued, 2), domain.EventWithdrawn, domain.ReasonIssued, true},
		{"списание", stored(domain.StatusInStock, 1), source(domain.StatusDiscarded, domain.ReasonDiscarded, 2), domain.EventWithdrawn, domain.ReasonDiscarded, true},
		{"истёк срок годности: статус DISCARDED, причина EXPIRED", stored(domain.StatusInStock, 1), source(domain.StatusDiscarded, domain.ReasonExpired, 2), domain.EventWithdrawn, domain.ReasonExpired, true},
		{"передача в другую организацию", stored(domain.StatusInStock, 1), source(domain.StatusTransferred, domain.ReasonTransferred, 2), domain.EventWithdrawn, domain.ReasonTransferred, true},
		{"причина не пришла: берётся по статусу", stored(domain.StatusInStock, 1), source(domain.StatusTransferred, "", 2), domain.EventWithdrawn, domain.ReasonTransferred, true},
		{"списание из карантина", stored(domain.StatusQuarantine, 1), source(domain.StatusDiscarded, domain.ReasonDiscarded, 2), domain.EventWithdrawn, domain.ReasonDiscarded, true},
		{"в карантин", stored(domain.StatusInStock, 1), source(domain.StatusQuarantine, "", 2), domain.EventUpdated, "", true},
		{"из карантина в наличие", stored(domain.StatusQuarantine, 1), source(domain.StatusInStock, "", 2), domain.EventUpdated, "", true},
		{"сменилось только место хранения", stored(domain.StatusInStock, 1), func() domain.SourceUnit {
			s := source(domain.StatusInStock, "", 2)
			s.StorageLocation = "холодильник 2"
			return s
		}(), domain.EventUpdated, "", false},
		{"выбывшая единица меняет статус выбытия: без события", stored(domain.StatusIssued, 1), source(domain.StatusDiscarded, domain.ReasonDiscarded, 2), domain.EventSilent, "", true},
		{"выбывшая единица вернулась в наличие: без события", stored(domain.StatusIssued, 1), source(domain.StatusInStock, "", 2), domain.EventSilent, "", true},
		{"выбывшая единица сменила место хранения: без события", stored(domain.StatusDiscarded, 1), source(domain.StatusDiscarded, domain.ReasonDiscarded, 2), domain.EventSilent, "", false},
		{"повтор той же версии", stored(domain.StatusInStock, 5), source(domain.StatusIssued, domain.ReasonIssued, 5), domain.EventNone, "", false},
		{"устаревшая версия", stored(domain.StatusIssued, 5), source(domain.StatusInStock, "", 4), domain.EventNone, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ch, err := domain.Apply(c.prev, c.in, newID, t0.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if ch.Event != c.event {
				t.Fatalf("событие %v, ожидали %v", ch.Event, c.event)
			}
			if ch.Reason != c.reason {
				t.Fatalf("причина %q, ожидали %q", ch.Reason, c.reason)
			}
			if ch.StatusChanged != c.statusChanged {
				t.Fatalf("смена статуса %v, ожидали %v", ch.StatusChanged, c.statusChanged)
			}
		})
	}
}

func TestApplyKeepsIdentityAndTakesNewState(t *testing.T) {
	ch, err := domain.Apply(nil, source(domain.StatusInStock, "", 3), newID, t0)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Unit.UnitID != newID || ch.Unit.Version != 3 || !ch.Unit.StatusReceivedAt.Equal(t0) {
		t.Fatalf("новая единица: %+v", ch.Unit)
	}

	in := source(domain.StatusQuarantine, "", 7)
	in.StorageLocation = "карантинный холодильник"
	ch, err = domain.Apply(stored(domain.StatusInStock, 6), in, newID, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if ch.Unit.UnitID != knownID {
		t.Fatalf("суррогатный unit_id сменился: %s", ch.Unit.UnitID)
	}
	if ch.Unit.Version != 7 || ch.Unit.Status != domain.StatusQuarantine || ch.Unit.StorageLocation != in.StorageLocation {
		t.Fatalf("состояние не обновлено: %+v", ch.Unit)
	}
	if !ch.Unit.StatusReceivedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("status_received_at %v", ch.Unit.StatusReceivedAt)
	}

	// Устаревшая версия возвращает прежнее состояние без изменений.
	prev := stored(domain.StatusIssued, 9)
	ch, err = domain.Apply(prev, source(domain.StatusInStock, "", 8), newID, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if ch.Unit != *prev {
		t.Fatalf("устаревшая версия изменила единицу: %+v", ch.Unit)
	}
}

func TestApplyRejectsMalformedSource(t *testing.T) {
	cases := map[string]domain.SourceUnit{
		"неизвестный статус":  source("LOST", "", 1),
		"нулевая версия":      source(domain.StatusInStock, "", 0),
		"неизвестная причина": source(domain.StatusDiscarded, "TRANSFUSED", 1),
		"нет кода АИСТ-М": func() domain.SourceUnit {
			s := source(domain.StatusInStock, "", 1)
			s.AistUnitCode = ""
			return s
		}(),
		"отрицательный объём": func() domain.SourceUnit {
			s := source(domain.StatusInStock, "", 1)
			s.VolumeML = -1
			return s
		}(),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := domain.Apply(nil, in, newID, t0)
			if !errors.Is(err, domain.ErrInvalidSource) {
				t.Fatalf("ожидали ErrInvalidSource, получили %v", err)
			}
		})
	}
}

func TestWithdrawReason(t *testing.T) {
	cases := []struct{ status, reason, want string }{
		{domain.StatusIssued, "", domain.ReasonIssued},
		{domain.StatusDiscarded, "", domain.ReasonDiscarded},
		{domain.StatusTransferred, "", domain.ReasonTransferred},
		{domain.StatusDiscarded, domain.ReasonExpired, domain.ReasonExpired},
		{domain.StatusIssued, domain.ReasonIssued, domain.ReasonIssued},
	}
	for _, c := range cases {
		if got := domain.WithdrawReason(c.status, c.reason); got != c.want {
			t.Errorf("WithdrawReason(%s, %q) = %s, ожидали %s", c.status, c.reason, got, c.want)
		}
	}
}

func TestProtectedVolumeValidation(t *testing.T) {
	ok := domain.ProtectedVolume{ClinicID: 1, ComponentKind: "RBC", VolumeUnits: 4, Reason: "демо"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []domain.ProtectedVolume{
		{ClinicID: 0, ComponentKind: "RBC", VolumeUnits: 1},
		{ClinicID: 1, ComponentKind: "", VolumeUnits: 1},
		{ClinicID: 1, ComponentKind: "RBC", VolumeUnits: -1},
	}
	for _, pv := range bad {
		if err := pv.Validate(); !errors.Is(err, domain.ErrInvalidProtectedVolume) {
			t.Errorf("%+v: ожидали ErrInvalidProtectedVolume, получили %v", pv, err)
		}
	}
}
