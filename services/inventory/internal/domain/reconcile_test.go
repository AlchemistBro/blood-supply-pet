package domain_test

import (
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

// Схема состояний (FR008): переходы вне схемы отмечаются расхождением, но копия всё равно принимает
// состояние источника — учёт ведётся в АИСТ-М.
func TestInvalidTransitionsAreMarked(t *testing.T) {
	cases := []struct {
		from, to string
		invalid  bool
	}{
		{domain.StatusInStock, domain.StatusQuarantine, false},
		{domain.StatusQuarantine, domain.StatusInStock, false},
		{domain.StatusInStock, domain.StatusIssued, false},
		{domain.StatusQuarantine, domain.StatusDiscarded, false},
		{domain.StatusQuarantine, domain.StatusIssued, true},
		{domain.StatusQuarantine, domain.StatusTransferred, true},
		{domain.StatusIssued, domain.StatusInStock, true},
		{domain.StatusIssued, domain.StatusDiscarded, true},
		{domain.StatusDiscarded, domain.StatusDiscarded, false},
	}
	for _, c := range cases {
		reason := ""
		if domain.IsWithdrawn(c.to) {
			reason = domain.WithdrawReason(c.to, "")
		}
		ch, err := domain.Apply(stored(c.from, 1), source(c.to, reason, 2), newID, t0)
		if err != nil {
			t.Fatal(err)
		}
		if ch.InvalidTransition != c.invalid || ch.From != c.from || ch.Unit.Status != c.to {
			t.Errorf("%s → %s: расхождение %v (ожидали %v), состояние %s", c.from, c.to, ch.InvalidTransition, c.invalid, ch.Unit.Status)
		}
	}
}

// Карантин → выдача вне схемы, но это выбытие: событие UnitWithdrawn и отметка расхождения.
func TestQuarantineIssuedIsWithdrawnAndInvalid(t *testing.T) {
	ch, err := domain.Apply(stored(domain.StatusQuarantine, 1), source(domain.StatusIssued, domain.ReasonIssued, 2), newID, t0)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Event != domain.EventWithdrawn || !ch.InvalidTransition || ch.Reason != domain.ReasonIssued {
		t.Fatalf("%+v", ch)
	}
}

// Сверка со снимком (FR001): нет в копии, копия отстала, та же версия с другими данными — расхождения;
// копия новее снимка или совпадает — нет.
func TestReconcileWithSnapshot(t *testing.T) {
	same := source(domain.StatusInStock, "", 5)
	moved := source(domain.StatusInStock, "", 5)
	moved.StorageLocation = "холодильник 9"
	cases := []struct {
		name  string
		prev  *domain.Unit
		in    domain.SourceUnit
		kind  domain.DiscrepancyKind
		event domain.EventKind
	}{
		{"нет в копии", nil, same, domain.DiscrepancyMissing, domain.EventRegistered},
		{"копия отстала", stored(domain.StatusInStock, 4), source(domain.StatusIssued, domain.ReasonIssued, 5), domain.DiscrepancyStale, domain.EventWithdrawn},
		{"та же версия, другое место", stored(domain.StatusInStock, 5), moved, domain.DiscrepancyMismatch, domain.EventUpdated},
		{"та же версия, другой статус", stored(domain.StatusInStock, 5), source(domain.StatusDiscarded, domain.ReasonDiscarded, 5), domain.DiscrepancyMismatch, domain.EventWithdrawn},
		{"совпадает", stored(domain.StatusInStock, 5), same, domain.DiscrepancyNone, domain.EventNone},
		{"копия новее снимка", stored(domain.StatusIssued, 6), same, domain.DiscrepancyNone, domain.EventNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ch, kind, err := domain.Reconcile(c.prev, c.in, newID, t0)
			if err != nil {
				t.Fatal(err)
			}
			if kind != c.kind || ch.Event != c.event {
				t.Fatalf("расхождение %q событие %v, ожидали %q %v", kind, ch.Event, c.kind, c.event)
			}
			if c.kind != domain.DiscrepancyNone && ch.Unit.StorageLocation != c.in.StorageLocation {
				t.Fatalf("копия не исправлена по снимку: %+v", ch.Unit)
			}
		})
	}
}
