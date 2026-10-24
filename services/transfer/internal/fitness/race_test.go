//go:build integration

package fitness

import (
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// Четыре гонки из obligation-states.md плюс NFR004 и частичное подтверждение (ConfirmationRaceFitnessTests.java).
// Обязательны на каждом этапе начиная с 1. Ослаблять их ради зелёного CI нельзя.

const rounds = 15

// Гонка 1 (NFR013, ADR-0027 п. 9): 60 единиц, защищённый объём 20, 50 параллельных подтверждений разных единиц —
// ровно 40 проходят, 10 упираются в защищённый объём; резерв сходится с закреплениями.
func TestRace1ParallelConfirmationsDoNotBreachProtectedVolume(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 60) // четыре группы вперемешку
	h.protect(1, "RBC", 20)
	tasks := make([]func() (any, error), 0, 50)
	for i := 0; i < 50; i++ {
		unit := units[i]
		o := h.open(1, "RBC", 1)
		tasks = append(tasks, func() (any, error) { return h.confirm(o, []uuid.UUID{unit}, false) })
	}
	results := race(t, tasks...)

	if n := countOf[domain.Confirmed](results); n != 40 {
		t.Errorf("Confirmed: %d, ожидали 40", n)
	}
	if n := countOf[domain.ProtectedVolumeExceeded](results); n != 10 {
		t.Errorf("ProtectedVolumeExceeded: %d, ожидали 10", n)
	}
	h.assertStockConsistent(1, "RBC")
}

// Гонка 1b (NFR004): 50 обязательств на одну единицу. Остаток 50 и защищённый объём 0, чтобы ограничивал
// только частичный уникальный индекс закреплений, а не NFR013.
func TestRace1bFiftyConfirmationsOfOneUnitAllocateItOnce(t *testing.T) {
	h := setup(t)
	unit := h.registerUnits(1, "PLT", 50)[0]
	tasks := make([]func() (any, error), 0, 50)
	for i := 0; i < 50; i++ {
		o := h.open(1, "PLT", 1)
		tasks = append(tasks, func() (any, error) { return h.confirm(o, []uuid.UUID{unit}, false) })
	}
	results := race(t, tasks...)

	if n := countOf[domain.Confirmed](results); n != 1 {
		t.Errorf("Confirmed: %d, ожидали ровно 1", n)
	}
	if n := countOf[domain.UnitsTaken](results); n != 49 {
		t.Errorf("UnitsTaken: %d, ожидали 49", n)
	}
	if n := h.count(`SELECT count(*) FROM allocation WHERE unit_id = $1 AND status = 'CONFIRMED'`, unit); n != 1 {
		t.Errorf("действующих закреплений единицы: %d", n)
	}
	h.assertStockConsistent(1, "PLT")
}

// unavailableDuringConfirmation — общая часть гонки 2 и гонки карантина: событие о недоступности единицы
// приходит в окне между проверкой АИСТ-М и транзакцией подтверждения. В чётных раундах событие обрабатывается
// параллельно с транзакцией, в нечётных — внутри окна до неё (детерминированный худший случай).
func unavailableDuringConfirmation(t *testing.T, makeUnavailable func(h *harness, unit uuid.UUID, clinic int64) error,
	broken domain.BreakReason) {
	h := setup(t)
	for round := 0; round < rounds; round++ {
		clinic := int64(round + 1)
		unit := h.registerUnits(clinic, "FFP", 1)[0]
		o := h.open(clinic, "FFP", 1)
		checked := make(chan struct{})
		var once sync.Once
		inWindow := round%2 == 1
		var hookErr error
		h.aist.setHook(func(ids []uuid.UUID) {
			if !slices.Contains(ids, unit) {
				return
			}
			once.Do(func() {
				if inWindow {
					hookErr = makeUnavailable(h, unit, clinic) // АИСТ-М уже ответила «на месте», событие пришло следом
				}
				close(checked)
			})
		})
		results := race(t,
			func() (any, error) { return h.confirm(o, []uuid.UUID{unit}, false) },
			func() (any, error) {
				<-checked
				if inWindow {
					return "в окне", nil
				}
				return "параллельно", makeUnavailable(h, unit, clinic)
			})
		h.aist.setHook(nil)
		if hookErr != nil {
			t.Fatalf("раунд %d: событие в окне: %v", round, hookErr)
		}

		if n := h.confirmedOnUnavailableUnits(); n != 0 {
			t.Fatalf("раунд %d: недоступная единица осталась закреплённой (%d)", round, n)
		}
		h.assertStockConsistent(clinic, "FFP")
		v := h.view(o)
		switch r := results[0].(type) {
		case domain.Confirmed:
			if v.Status != domain.StatusBroken || v.StatusReason != string(broken) {
				t.Fatalf("раунд %d: подтверждено, затем %s (%s), ожидали BROKEN (%s)", round, v.Status, v.StatusReason, broken)
			}
		case domain.UnitsUnavailable:
			if v.Status != domain.StatusPending {
				t.Fatalf("раунд %d: UnitsUnavailable, статус %s, ожидали PENDING", round, v.Status)
			}
		case domain.Rejected:
			if v.Status != domain.StatusRejected {
				t.Fatalf("раунд %d: Rejected, статус %s", round, v.Status)
			}
		default:
			t.Fatalf("раунд %d: неожиданный исход %#v", round, r)
		}
		if inWindow && !isType[domain.UnitsUnavailable](results[0]) {
			t.Fatalf("раунд %d: событие пришло до транзакции, перепроверка копии под блокировкой должна была поймать: %#v", round, results[0])
		}
	}
}

// Гонка 2: выдача единицы во время подтверждения. Закреплённой выбывшей единицы не остаётся ни в одном исходе.
func TestRace2UnitWithdrawnDuringConfirmationNeverStaysAllocated(t *testing.T) {
	unavailableDuringConfirmation(t, func(h *harness, unit uuid.UUID, clinic int64) error {
		return h.withdraw(unit, clinic, "FFP")
	}, domain.BreakUnitWithdrawn)
}

// То же для карантина (ПП РФ № 641, п. 62 а): закреплённой карантинной единицы не бывает.
func TestRaceQuarantineDuringConfirmationNeverStaysAllocated(t *testing.T) {
	unavailableDuringConfirmation(t, func(h *harness, unit uuid.UUID, clinic int64) error {
		return h.quarantine(unit, clinic, "FFP")
	}, domain.BreakUnitQuarantined)
}

// Гонка 3: позднее подтверждение против таймера эскалации. По заявке всегда ровно одно живое задание;
// проигравшее подтверждение получает TaskWithdrawn(EXPIRED), единицы дважды не закрепляются.
func TestRace3LateConfirmationAfterEscalationNeverShipsTwice(t *testing.T) {
	h := setup(t)
	for round := 0; round < rounds; round++ {
		first, next := int64(round+1), int64(round+41)
		unit := h.registerUnits(first, "RBC", 1)[0]
		o := h.open(first, "RBC", 1, next)
		requestID := h.view(o).RequestID
		h.clock.Advance(16 * time.Minute)

		results := race(t,
			func() (any, error) { return h.confirm(o, []uuid.UUID{unit}, false) },
			func() (any, error) { return h.svc.ExpireOverdue(h.ctx) })

		list, err := h.svc.ByRequest(h.ctx, requestID)
		if err != nil {
			t.Fatal(err)
		}
		var live []app.ObligationView
		for _, v := range list {
			if !v.Status.IsTerminal() {
				live = append(live, v)
			}
		}
		if len(live) != 1 {
			t.Fatalf("раунд %d: живых заданий по заявке %d, ожидали ровно одно", round, len(live))
		}
		switch r := results[0].(type) {
		case domain.Confirmed:
			if live[0].ID != o {
				t.Fatalf("раунд %d: подтверждено, но живо другое задание", round)
			}
		default:
			if !reflect.DeepEqual(r, domain.TaskWithdrawn{ObligationID: o, Status: domain.StatusExpired}) {
				t.Fatalf("раунд %d: ожидали TaskWithdrawn(EXPIRED), получили %#v", round, r)
			}
			if live[0].SourceClinicID != next {
				t.Fatalf("раунд %d: живое задание у источника %d, ожидали %d", round, live[0].SourceClinicID, next)
			}
		}
		if n := h.count(`SELECT count(*) FROM allocation WHERE unit_id = $1 AND status = 'CONFIRMED'`, unit); n > 1 {
			t.Fatalf("раунд %d: единица закреплена %d раз", round, n)
		}
		h.assertStockConsistent(first, "RBC")
	}
}

// Гонка 4: замена в A против подтверждения в B одной и той же единицы — уникальный индекс пускает одного.
func TestRace4ReplacementAgainstConfirmationOfTheSameUnit(t *testing.T) {
	h := setup(t)
	for round := 0; round < rounds; round++ {
		clinic := int64(round + 1)
		units := h.registerUnits(clinic, "RBC", 2)
		held, contested := units[0], units[1]
		a := h.open(clinic, "RBC", 1)
		if res := h.mustConfirm(a, []uuid.UUID{held}, false); !isType[domain.Confirmed](res) {
			t.Fatalf("раунд %d: подтверждение A: %#v", round, res)
		}
		b := h.open(clinic, "RBC", 1)

		results := race(t,
			func() (any, error) {
				return h.svc.Replace(h.ctx, app.ReplaceUnit{ObligationID: a, OldUnitID: held, NewUnitID: contested, Operator: "operator-1"})
			},
			func() (any, error) { return h.confirm(b, []uuid.UUID{contested}, false) })

		replacement, confirmation := results[0], results[1]
		if _, ok := replacement.(domain.Replaced); ok {
			if !isType[domain.UnitsTaken](confirmation) {
				t.Fatalf("раунд %d: замена прошла, подтверждение B: %#v, ожидали UnitsTaken", round, confirmation)
			}
			if u := h.view(a).Units; !slices.Equal(u, []uuid.UUID{contested}) {
				t.Fatalf("раунд %d: A держит %v", round, u)
			}
		} else {
			if !reflect.DeepEqual(replacement, domain.UnitTaken{UnitID: contested}) {
				t.Fatalf("раунд %d: замена: %#v, ожидали UnitTaken", round, replacement)
			}
			if !isType[domain.Confirmed](confirmation) {
				t.Fatalf("раунд %d: подтверждение B: %#v, ожидали Confirmed", round, confirmation)
			}
			if u := h.view(a).Units; !slices.Equal(u, []uuid.UUID{held}) {
				t.Fatalf("раунд %d: A держит %v", round, u)
			}
		}
		if n := h.count(`SELECT count(*) FROM allocation WHERE unit_id = $1 AND status = 'CONFIRMED'`, contested); n != 1 {
			t.Fatalf("раунд %d: оспариваемая единица закреплена %d раз", round, n)
		}
		h.assertStockConsistent(clinic, "RBC")
	}
}

// Частичное подтверждение и остаток у другого источника: ни одна единица не уходит дважды.
func TestFitnessPartialConfirmationAndRemainderNeverShipOneUnitTwice(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 4)
	otherUnits := h.registerUnits(2, "RBC", 2)
	o := h.open(1, "RBC", 4)
	h.aist.set(units[3], domain.UnitIssued) // в АИСТ-М уже выдана, проекция ещё не знает

	if res := h.mustConfirm(o, units, false); !reflect.DeepEqual(res, domain.UnitsUnavailable{Units: []uuid.UUID{units[3]}}) {
		t.Fatalf("без частичного: %#v", res)
	}
	if s := h.view(o).Status; s != domain.StatusPending {
		t.Fatalf("статус %s, ожидали PENDING", s)
	}
	partial := h.mustConfirm(o, units, true)
	if !reflect.DeepEqual(partial, domain.Confirmed{ObligationID: o, Units: units[:3], Shortfall: 1}) {
		t.Fatalf("частичное: %#v", partial)
	}
	if q := h.view(o).ConfirmedQty; q != 3 {
		t.Fatalf("confirmed_qty %d", q)
	}
	if got := h.eventTypes(o); !slices.Equal(got, []string{"ObligationOpened", "TransferConfirmed", "TransferShortfallRecorded"}) {
		t.Fatalf("события: %v", got)
	}

	// Подбор предлагает остаток другому источнику: новое задание только на одну единицу.
	remainder := h.open(2, "RBC", 1)
	if res := h.mustConfirm(remainder, []uuid.UUID{units[0]}, false); !isType[domain.InvalidRequest](res) {
		t.Fatalf("единица первого источника ушла бы второй раз: %#v", res)
	}
	if res := h.mustConfirm(remainder, []uuid.UUID{otherUnits[0]}, false); !isType[domain.Confirmed](res) {
		t.Fatalf("остаток: %#v", res)
	}
	shipped := append(append([]uuid.UUID{}, h.view(o).Units...), h.view(remainder).Units...)
	if len(shipped) != 4 {
		t.Fatalf("отгружается %d единиц, ожидали 4", len(shipped))
	}
	seen := map[uuid.UUID]bool{}
	for _, u := range shipped {
		if seen[u] {
			t.Fatalf("единица %s уходит дважды", u)
		}
		seen[u] = true
	}
}

func isType[T any](v any) bool {
	_, ok := v.(T)
	return ok
}
