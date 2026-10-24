package domain_test

import (
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// Разрешённые переходы ровно по docs/domain/obligation-states.md. Всё, чего нет в таблице, запрещено.
var allowed = map[domain.Status][]domain.Status{
	domain.StatusPending:            {domain.StatusConfirmed, domain.StatusRejected, domain.StatusExpired, domain.StatusCancelled},
	domain.StatusConfirmed:          {domain.StatusTransportRequested, domain.StatusCancelled, domain.StatusBroken, domain.StatusConfirmed},
	domain.StatusTransportRequested: {domain.StatusCancelled, domain.StatusBroken, domain.StatusTransportRequested, domain.StatusDispatched},
	domain.StatusDispatched:         {domain.StatusDelivered},
}

func TestEveryTransitionMatchesStateDiagram(t *testing.T) {
	for _, from := range domain.AllStatuses {
		for _, to := range domain.AllStatuses {
			want := false
			for _, s := range allowed[from] {
				if s == to {
					want = true
				}
			}
			if got := domain.CanTransition(from, to); got != want {
				t.Errorf("%s → %s: разрешён=%v, по канону %v", from, to, got, want)
			}
			err := domain.CheckTransition(from, to)
			if want && err != nil {
				t.Errorf("%s → %s: ожидали разрешение, получили %v", from, to, err)
			}
			if !want && err == nil {
				t.Errorf("%s → %s: запрещённый переход прошёл проверку", from, to)
			}
		}
	}
}

func TestAllStatusesAreNine(t *testing.T) {
	if len(domain.AllStatuses) != 9 {
		t.Fatalf("статусов O07 %d, по канону 9", len(domain.AllStatuses))
	}
	for _, s := range domain.AllStatuses {
		got, err := domain.ParseStatus(string(s))
		if err != nil || got != s {
			t.Errorf("ParseStatus(%q) = %q, %v", s, got, err)
		}
	}
	if _, err := domain.ParseStatus("WAITING_REPLACEMENT"); err == nil {
		t.Error("статуса «ожидает замены» нет (решение владельца 23.10.2026), разбор должен падать")
	}
}

func TestTerminalStatusesHaveNoWayOut(t *testing.T) {
	terminal := map[domain.Status]bool{
		domain.StatusRejected: true, domain.StatusExpired: true, domain.StatusCancelled: true,
		domain.StatusBroken: true, domain.StatusDelivered: true,
	}
	for _, s := range domain.AllStatuses {
		if s.IsTerminal() != terminal[s] {
			t.Errorf("%s: конечный=%v, по канону %v", s, s.IsTerminal(), terminal[s])
		}
		if terminal[s] {
			for _, to := range domain.AllStatuses {
				if domain.CanTransition(s, to) {
					t.Errorf("из конечного %s есть переход в %s", s, to)
				}
			}
		}
	}
}

func TestStatusGroups(t *testing.T) {
	cases := []struct {
		name string
		fn   func(domain.Status) bool
		want []domain.Status
	}{
		{"держит закрепления", domain.Status.HoldsAllocations, []domain.Status{domain.StatusConfirmed, domain.StatusTransportRequested}},
		{"замена разрешена (ADR-0011)", domain.Status.Replaceable, []domain.Status{domain.StatusConfirmed, domain.StatusTransportRequested}},
		{"отмена разрешена", domain.Status.Cancellable, []domain.Status{domain.StatusPending, domain.StatusConfirmed, domain.StatusTransportRequested}},
		{"нарушение выбытием", domain.Status.Breakable, []domain.Status{domain.StatusConfirmed, domain.StatusTransportRequested}},
	}
	for _, c := range cases {
		in := map[domain.Status]bool{}
		for _, s := range c.want {
			in[s] = true
		}
		for _, s := range domain.AllStatuses {
			if c.fn(s) != in[s] {
				t.Errorf("%s: %s = %v, ожидали %v", c.name, s, c.fn(s), in[s])
			}
		}
	}
}

// Каждая группа статусов согласована с таблицей переходов: иначе CAS по группе пропустит запрещённый переход.
func TestStatusGroupsAgreeWithTransitions(t *testing.T) {
	for _, s := range domain.AllStatuses {
		if s.Cancellable() && !domain.CanTransition(s, domain.StatusCancelled) {
			t.Errorf("%s отменяемый, но перехода в CANCELLED нет", s)
		}
		if s.Breakable() && !domain.CanTransition(s, domain.StatusBroken) {
			t.Errorf("%s нарушаемый, но перехода в BROKEN нет", s)
		}
		if s.Replaceable() && !domain.CanTransition(s, s) {
			t.Errorf("%s допускает замену, но перехода в себя нет", s)
		}
	}
	for _, s := range domain.StatusesCancellable() {
		if !s.Cancellable() {
			t.Errorf("StatusesCancellable содержит %s", s)
		}
	}
	for _, s := range domain.StatusesBreakable() {
		if !s.Breakable() {
			t.Errorf("StatusesBreakable содержит %s", s)
		}
	}
}
