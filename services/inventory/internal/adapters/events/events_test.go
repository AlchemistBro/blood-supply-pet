package events_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/adapters/events"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

var t0 = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)

func apply(t *testing.T, prev *domain.Unit, status, reason string, version int64) domain.Change {
	t.Helper()
	in := domain.SourceUnit{
		AistUnitCode: "AIST-SECRET-0001", ClinicID: 3, ComponentKind: "RBC", BloodGroup: "O-", RhPositive: false,
		Phenotype: "CcDee", VolumeML: 280, ExpiresAt: t0.Add(72 * time.Hour), StorageLocation: "холодильник 2",
		Status: status, WithdrawalReason: reason, Version: version, ChangedAt: t0,
	}
	ch, err := domain.Apply(prev, in, uuid.MustParse("01920000-0000-7000-8000-000000000007"), t0)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

func TestStatusChangesMapToContractEvents(t *testing.T) {
	reg := apply(t, nil, domain.StatusInStock, "", 1)
	cases := []struct {
		name   string
		ch     domain.Change
		typ    string
		fields map[string]any
	}{
		{"новая единица", reg, "UnitRegistered", map[string]any{"status": "IN_STOCK", "blood_group": "O-", "rh_positive": false, "clinic_id": float64(3), "version": float64(1)}},
		{"карантин", apply(t, &reg.Unit, domain.StatusQuarantine, "", 2), "UnitUpdated", map[string]any{"status": "QUARANTINE", "version": float64(2)}},
		{"выдача", apply(t, &reg.Unit, domain.StatusIssued, domain.ReasonIssued, 2), "UnitWithdrawn", map[string]any{"reason": "ISSUED"}},
		{"истёк срок", apply(t, &reg.Unit, domain.StatusDiscarded, domain.ReasonExpired, 2), "UnitWithdrawn", map[string]any{"reason": "EXPIRED"}},
		{"передача", apply(t, &reg.Unit, domain.StatusTransferred, domain.ReasonTransferred, 2), "UnitWithdrawn", map[string]any{"reason": "TRANSFERRED"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, ok := events.Payload(c.ch)
			if !ok {
				t.Fatal("событие не построено")
			}
			if p.MessageType() != c.typ {
				t.Fatalf("тип %s, ожидали %s", p.MessageType(), c.typ)
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "AIST-SECRET") || strings.Contains(string(raw), "aist_unit_code") {
				t.Fatalf("код единицы АИСТ-М попал в событие (ADR-0010): %s", raw)
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			if m["unit_id"] != "01920000-0000-7000-8000-000000000007" {
				t.Fatalf("unit_id: %v", m["unit_id"])
			}
			for k, v := range c.fields {
				if m[k] != v {
					t.Errorf("%s = %v, ожидали %v", k, m[k], v)
				}
			}
		})
	}
}

func TestStaleChangeHasNoEvent(t *testing.T) {
	reg := apply(t, nil, domain.StatusIssued, domain.ReasonIssued, 5)
	stale := apply(t, &reg.Unit, domain.StatusInStock, "", 4)
	if _, ok := events.Payload(stale); ok {
		t.Fatal("устаревшая версия не должна давать событие")
	}
}

// Новая версия уже выбывшей единицы не публикуется: UnitUpdated допускает только IN_STOCK и QUARANTINE,
// а выбытие уже опубликовано UnitWithdrawn.
func TestWithdrawnUnitChangeHasNoEvent(t *testing.T) {
	reg := apply(t, nil, domain.StatusInStock, "", 1)
	out := apply(t, &reg.Unit, domain.StatusIssued, domain.ReasonIssued, 2)
	for _, next := range []domain.Change{
		apply(t, &out.Unit, domain.StatusDiscarded, domain.ReasonDiscarded, 3),
		apply(t, &out.Unit, domain.StatusInStock, "", 3),
	} {
		if next.Event != domain.EventSilent {
			t.Fatalf("событие %v, ожидали silent", next.Event)
		}
		if p, ok := events.Payload(next); ok {
			t.Fatalf("для выбывшей единицы построено событие %s", p.MessageType())
		}
	}
}

func TestProtectedVolumeKeyAndReasonCodes(t *testing.T) {
	pv := domain.ProtectedVolume{ClinicID: 12, ComponentKind: "PLT", VolumeUnits: 3}
	if pv.Key() != "12:PLT" {
		t.Fatalf("ключ %q", pv.Key())
	}
	// Коды причин домена совпадают с опубликованным контрактом.
	for _, r := range []string{domain.ReasonIssued, domain.ReasonDiscarded, domain.ReasonTransferred, domain.ReasonExpired} {
		switch r {
		case inventory.WithdrawIssued, inventory.WithdrawDiscarded, inventory.WithdrawTransferred, inventory.WithdrawExpired:
		default:
			t.Errorf("причина %s не из контракта", r)
		}
	}
}
