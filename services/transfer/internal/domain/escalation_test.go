package domain_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// choice_id нового O07 при эскалации детерминирован: повтор таймера не откроет второе задание.
func TestEscalationChoiceIDIsDeterministicV5(t *testing.T) {
	old := uuid.MustParse("01920000-0000-7000-8000-000000000001")
	a := domain.EscalationChoiceID(old)
	if a != domain.EscalationChoiceID(old) {
		t.Fatal("один и тот же id даёт разные choice_id")
	}
	if a.Version() != 5 {
		t.Errorf("версия uuid %d, ожидали 5", a.Version())
	}
	if a == domain.EscalationChoiceID(uuid.New()) {
		t.Error("разные обязательства дают одинаковый choice_id")
	}
	want := uuid.NewSHA1(domain.EscalationNamespace, []byte("escalation:"+old.String()))
	if a != want {
		t.Errorf("choice_id %s, ожидали uuid5(пространство transfer, \"escalation:\"+id) = %s", a, want)
	}
	// Пространство зафиксировано: его смена сломает идемпотентность для уже открытых эскалаций.
	if domain.EscalationNamespace.String() != "6f1c3a52-9b0e-4d57-8a43-1e2b7c9d0f15" {
		t.Errorf("пространство имён изменено: %s", domain.EscalationNamespace)
	}
}

func TestNextSource(t *testing.T) {
	next, rest, ok := domain.NextSource([]int64{4, 5, 6}, 9)
	if !ok || next != 4 || !reflect.DeepEqual(rest, []int64{5, 6}) {
		t.Errorf("следующий источник: %d %v %v", next, rest, ok)
	}
	if _, _, ok := domain.NextSource(nil, 9); ok {
		t.Error("без запасных источников эскалации к источнику нет")
	}
	// Получатель не может быть источником (CHECK source ≠ target): такой элемент пропускается.
	next, rest, ok = domain.NextSource([]int64{9, 5}, 9)
	if !ok || next != 5 || len(rest) != 0 {
		t.Errorf("пропуск получателя: %d %v %v", next, rest, ok)
	}
	if _, _, ok := domain.NextSource([]int64{9}, 9); ok {
		t.Error("единственный запасной источник — сам получатель: эскалации к источнику нет")
	}
}

func TestDeadline(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	if got := domain.Deadline(now, 15*time.Minute); !got.Equal(now.Add(15 * time.Minute)) {
		t.Errorf("срок %s", got)
	}
}

func TestOpenValidation(t *testing.T) {
	ok := domain.NewObligation{
		ChoiceID: uuid.New(), RequestID: uuid.New(), Origin: domain.OriginDoctorChoice, SourceClinicID: 1, TargetClinicID: 2,
		ComponentKind: "RBC", RequestedQty: 1, UrgencyCode: "URGENT", ConfirmWindow: time.Minute,
		Spec: domain.ReplacementSpec{RuleVersion: "O15-v3"},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("корректная команда: %v", err)
	}
	cases := map[string]func(*domain.NewObligation){
		"нет choice_id":             func(n *domain.NewObligation) { n.ChoiceID = uuid.Nil },
		"нет request_id":            func(n *domain.NewObligation) { n.RequestID = uuid.Nil },
		"источник = получатель":     func(n *domain.NewObligation) { n.TargetClinicID = 1 },
		"ноль единиц":               func(n *domain.NewObligation) { n.RequestedQty = 0 },
		"нет вида":                  func(n *domain.NewObligation) { n.ComponentKind = "" },
		"неизвестное происхождение": func(n *domain.NewObligation) { n.Origin = "MANUAL" },
		"отрицательный срок":        func(n *domain.NewObligation) { n.ConfirmWindow = -time.Second },
		"нет версии правила":        func(n *domain.NewObligation) { n.Spec.RuleVersion = "" },
	}
	for name, edit := range cases {
		n := ok
		edit(&n)
		if err := n.Validate(); err == nil {
			t.Errorf("%s: ожидали ошибку", name)
		}
	}
}
