package kafka_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/adapters/kafka"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/domain"
)

func command() logistics.RequestTransport {
	return logistics.RequestTransport{
		ObligationID: uuid.New(), RequestID: uuid.New(), SourceClinicID: 1, TargetClinicID: 2,
		ComponentKind: "RBC", Quantity: 2, TemperatureRegime: logistics.RegimeCold2to6,
	}
}

func TestRequestFromKeepsRouteAndRegime(t *testing.T) {
	c := command()
	r, err := kafka.RequestFrom(c)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Request{
		ObligationID: c.ObligationID, RequestID: c.RequestID, SourceClinicID: 1, TargetClinicID: 2,
		ComponentKindCode: "RBC", Quantity: 2, TemperatureRegime: logistics.RegimeCold2to6,
	}
	if r != want {
		t.Fatalf("заказ %+v, ожидали %+v", r, want)
	}
}

// Без режима в команде он выводится из вида компонента (демо, ПП РФ № 641 — условия транспортировки).
func TestMissingRegimeIsDerivedFromComponentKind(t *testing.T) {
	for kind, want := range map[string]string{
		"RBC": logistics.RegimeCold2to6, "FFP": logistics.RegimeFrozenMinus25,
		"CRYO": logistics.RegimeFrozenMinus25, "PLT": logistics.RegimeRoom20to24,
	} {
		c := command()
		c.ComponentKind, c.TemperatureRegime = kind, ""
		r, err := kafka.RequestFrom(c)
		if err != nil || r.TemperatureRegime != want {
			t.Errorf("%s: режим %q, ошибка %v; ожидали %s", kind, r.TemperatureRegime, err, want)
		}
	}
}

func TestUnknownRegimeIsRejected(t *testing.T) {
	c := command()
	c.ComponentKind, c.TemperatureRegime = "XYZ", ""
	if _, err := kafka.RequestFrom(c); !errors.Is(err, kafka.ErrUnknownRegime) {
		t.Fatalf("неизвестный вид без режима: %v", err)
	}
	c = command()
	c.TemperatureRegime = "HOT"
	if _, err := kafka.RequestFrom(c); !errors.Is(err, kafka.ErrUnknownRegime) {
		t.Fatalf("неизвестный режим: %v", err)
	}
}

// Ядовитые команды уходят в DLT без повторов и без обращения к шлюзу (он здесь nil).
func TestPoisonCommandsArePermanent(t *testing.T) {
	h := kafka.Handler(nil)
	noRoute, _ := json.Marshal(logistics.RequestTransport{ObligationID: uuid.New(), RequestID: uuid.New()})
	noObligation, _ := json.Marshal(logistics.CancelTransport{Reason: logistics.CancelUnitWithdrawn})
	cases := map[string]messaging.Message{
		"неизвестный тип":          {ID: uuid.New(), Type: "SomethingElse", Payload: []byte(`{}`)},
		"битый JSON заказа":        {ID: uuid.New(), Type: "RequestTransport", Payload: []byte(`{`)},
		"заказ без маршрута":       {ID: uuid.New(), Type: "RequestTransport", Payload: noRoute},
		"битый JSON отмены":        {ID: uuid.New(), Type: "CancelTransport", Payload: []byte(`{`)},
		"отмена без обязательства": {ID: uuid.New(), Type: "CancelTransport", Payload: noObligation},
	}
	for name, msg := range cases {
		if err := h(context.Background(), msg); !messaging.IsPermanent(err) {
			t.Errorf("%s: ожидали постоянную ошибку, получили %v", name, err)
		}
	}
}
