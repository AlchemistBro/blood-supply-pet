package domain_test

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func ids(n int) []uuid.UUID {
	out := make([]uuid.UUID, n)
	for i := range out {
		out[i] = uuid.New()
	}
	return out
}

// Восемь вариантов ответа оператору (obligation-states.md), у каждого свой код и понятный текст.
func TestEightConfirmationResults(t *testing.T) {
	o := uuid.New()
	results := []domain.ConfirmationResult{
		domain.Confirmed{ObligationID: o, Units: ids(1)},
		domain.TaskWithdrawn{ObligationID: o, Status: domain.StatusExpired},
		domain.UnitsUnavailable{Units: ids(1)},
		domain.Rejected{ObligationID: o, Reason: domain.RejectUnitsUnavailable},
		domain.UnitsTaken{Units: ids(1)},
		domain.ProtectedVolumeExceeded{MaxConfirmable: 2},
		domain.InvalidRequest{Reason: "пустой список"},
		domain.SourceSystemUnavailable{},
	}
	codes := map[string]bool{}
	for _, r := range results {
		if r.Code() == "" || r.Message() == "" {
			t.Errorf("%T: пустой код или текст", r)
		}
		if codes[r.Code()] {
			t.Errorf("%T: код %s повторяется", r, r.Code())
		}
		codes[r.Code()] = true
	}
	want := []string{"CONFIRMED", "TASK_WITHDRAWN", "UNITS_UNAVAILABLE", "REJECTED", "UNITS_TAKEN",
		"PROTECTED_VOLUME_EXCEEDED", "INVALID_REQUEST", "SOURCE_SYSTEM_UNAVAILABLE"}
	if !reflect.DeepEqual(domain.ConfirmationCodes, want) {
		t.Errorf("ConfirmationCodes = %v, ожидали %v", domain.ConfirmationCodes, want)
	}
	for _, c := range want {
		if !codes[c] {
			t.Errorf("нет варианта с кодом %s", c)
		}
	}
}

func TestConfirmedShortfallAndPartial(t *testing.T) {
	c := domain.Confirmed{Units: ids(2), Shortfall: 1}
	if !c.Partial() {
		t.Error("недостача 1 — частичное подтверждение")
	}
	if (domain.Confirmed{Units: ids(3)}).Partial() {
		t.Error("без недостачи подтверждение полное")
	}
	if got := domain.Shortfall(5, 3); got != 2 {
		t.Errorf("Shortfall(5,3) = %d", got)
	}
	if got := domain.Shortfall(3, 3); got != 0 {
		t.Errorf("Shortfall(3,3) = %d", got)
	}
}

func TestValidateUnitSelection(t *testing.T) {
	two := ids(2)
	cases := []struct {
		name      string
		requested int
		units     []uuid.UUID
		valid     bool
	}{
		{"пустой список", 2, nil, false},
		{"повтор единицы", 2, []uuid.UUID{two[0], two[0]}, false},
		{"больше, чем в задании", 1, two, false},
		{"столько, сколько в задании", 2, two, true},
		{"меньше, чем в задании", 3, two, true},
	}
	for _, c := range cases {
		res := domain.ValidateUnitSelection(c.requested, c.units)
		if (res == nil) != c.valid {
			t.Errorf("%s: результат %v, ожидали валидность %v", c.name, res, c.valid)
		}
	}
}

func TestValidateOwnership(t *testing.T) {
	u := ids(3)
	copies := map[uuid.UUID]domain.UnitCopy{
		u[0]: {UnitID: u[0], ClinicID: 1, ComponentKind: "RBC"},
		u[1]: {UnitID: u[1], ClinicID: 2, ComponentKind: "RBC"},
		u[2]: {UnitID: u[2], ClinicID: 1, ComponentKind: "FFP"},
	}
	if res := domain.ValidateOwnership(1, "RBC", u[:1], copies); res != nil {
		t.Errorf("своя единица того же вида: %v", res)
	}
	for name, sel := range map[string][]uuid.UUID{
		"чужой источник": {u[0], u[1]},
		"другой вид":     {u[2]},
		"нет в копии":    {uuid.New()},
	} {
		if res := domain.ValidateOwnership(1, "RBC", sel, copies); res == nil {
			t.Errorf("%s: ожидали InvalidRequest", name)
		}
	}
}

// Карантин и отсутствие ответа по единице считаются недоступностью, как выбытие.
func TestSplitByStatusTreatsQuarantineAsUnavailable(t *testing.T) {
	u := ids(5)
	statuses := map[uuid.UUID]domain.UnitStatus{
		u[0]: domain.UnitInStock,
		u[1]: domain.UnitQuarantine,
		u[2]: domain.UnitIssued,
		u[3]: domain.UnitInStock,
		// u[4] нет в ответе
	}
	present, unavailable := domain.SplitByStatus(u, statuses)
	if !reflect.DeepEqual(present, []uuid.UUID{u[0], u[3]}) {
		t.Errorf("на месте: %v", present)
	}
	if !reflect.DeepEqual(unavailable, []uuid.UUID{u[1], u[2], u[4]}) {
		t.Errorf("недоступны: %v", unavailable)
	}
}

func TestDecideAfterSourceCheck(t *testing.T) {
	u := ids(2)
	cases := []struct {
		name        string
		present     []uuid.UUID
		unavailable []uuid.UUID
		partial     bool
		want        domain.CheckDecision
	}{
		{"все выбыли", nil, u, true, domain.DecideReject},
		{"часть выбыла, частичное не выбрано", u[:1], u[1:], false, domain.DecideUnitsUnavailable},
		{"часть выбыла, частичное выбрано", u[:1], u[1:], true, domain.DecideProceed},
		{"все на месте", u, nil, false, domain.DecideProceed},
	}
	for _, c := range cases {
		if got := domain.DecideAfterSourceCheck(c.present, c.unavailable, c.partial); got != c.want {
			t.Errorf("%s: %v, ожидали %v", c.name, got, c.want)
		}
	}
}

// Перепроверка копии под блокировкой Остатка (гонка 2): выдача, пришедшая событием во время вызова АИСТ-М.
func TestRecheckUnderLock(t *testing.T) {
	u := ids(3)
	present, res := domain.RecheckUnderLock(u, nil, false)
	if res != nil || !reflect.DeepEqual(present, u) {
		t.Errorf("без изменений в копии: %v, %v", present, res)
	}
	if _, res := domain.RecheckUnderLock(u, u[:1], false); res == nil || !reflect.DeepEqual(res.Units, u[:1]) {
		t.Errorf("частичное не выбрано: ожидали UnitsUnavailable(%v), получили %v", u[:1], res)
	}
	present, res = domain.RecheckUnderLock(u, u[:1], true)
	if res != nil || !reflect.DeepEqual(present, u[1:]) {
		t.Errorf("частичное выбрано: %v, %v", present, res)
	}
	if _, res := domain.RecheckUnderLock(u, u, true); res == nil {
		t.Error("все выбыли под блокировкой: ожидали UnitsUnavailable даже при частичном")
	}
}

// NFR013: free − k ≥ защищённый объём. Граница включительно.
func TestProtectedVolume(t *testing.T) {
	s := domain.Stock{Available: 10, Reserved: 3, Protected: 4}
	if s.Free() != 7 || s.MaxConfirmable() != 3 {
		t.Fatalf("free=%d max=%d", s.Free(), s.MaxConfirmable())
	}
	if res := s.CheckProtected(3); res != nil {
		t.Errorf("ровно до защищённого объёма можно: %v", res)
	}
	res := s.CheckProtected(4)
	if res == nil || res.MaxConfirmable != 3 {
		t.Errorf("ниже защищённого объёма нельзя: %v", res)
	}
	if (domain.Stock{Available: 2, Reserved: 2, Protected: 5}).MaxConfirmable() != 0 {
		t.Error("MaxConfirmable не бывает отрицательным")
	}
}

// Повтор того же запроса оператора возвращает тот же ответ, пока O07 держит закрепления.
func TestConfirmReplay(t *testing.T) {
	cases := []struct {
		status     domain.Status
		stored, in string
		want       bool
	}{
		{domain.StatusConfirmed, "k1", "k1", true},
		{domain.StatusTransportRequested, "k1", "k1", true},
		{domain.StatusConfirmed, "k1", "k2", false},
		{domain.StatusConfirmed, "", "", false},
		{domain.StatusExpired, "k1", "k1", false},
		{domain.StatusBroken, "k1", "k1", false},
		{domain.StatusPending, "", "k1", false},
	}
	for _, c := range cases {
		if got := domain.IsConfirmReplay(c.status, c.stored, c.in); got != c.want {
			t.Errorf("%s %q/%q: %v, ожидали %v", c.status, c.stored, c.in, got, c.want)
		}
	}
}
