package kafka_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/adapters/kafka"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func handlers() *kafka.Handlers {
	return kafka.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func msg(typ, payload string) messaging.Message {
	return messaging.Message{ID: uuid.New(), Type: typ, Topic: "test", Payload: []byte(payload)}
}

// Ядовитые сообщения — постоянная ошибка (DLT), чужие известные типы — пропуск.
func TestMessageClassification(t *testing.T) {
	h := handlers()
	ctx := context.Background()
	cases := []struct {
		name      string
		run       func() error
		permanent bool
	}{
		{"неизвестный тип в командах", func() error { return h.Commands(ctx, msg("Hack", `{}`)) }, true},
		{"неизвестный тип в inventory", func() error { return h.Inventory(ctx, msg("Hack", `{}`)) }, true},
		{"неизвестный тип в ответах", func() error { return h.Logistics(ctx, msg("Hack", `{}`)) }, true},
		{"битый JSON команды", func() error { return h.Commands(ctx, msg("OpenObligation", `{`)) }, true},
		{"битый JSON события", func() error { return h.Inventory(ctx, msg("UnitWithdrawn", `{`)) }, true},
		{"битый JSON ответа", func() error { return h.Logistics(ctx, msg("TransportAccepted", `{`)) }, true},
		{"доставка без идентификатора и времени", func() error { return h.Logistics(ctx, msg("TransportDelivered", `{}`)) }, true},
		{"отправка без идентификатора и времени", func() error { return h.Logistics(ctx, msg("TransportDispatched", `{}`)) }, true},
		{"доставка без идентификатора", func() error {
			return h.Logistics(ctx, msg("TransportDelivered", `{"delivered_at":"2026-10-24T12:00:00Z"}`))
		}, true},
		{"доставка без времени", func() error {
			return h.Logistics(ctx, msg("TransportDelivered", `{"obligation_id":"01920000-0000-7000-8000-000000000001"}`))
		}, true},
		{"отправка без идентификатора", func() error {
			return h.Logistics(ctx, msg("TransportDispatched", `{"dispatched_at":"2026-10-24T12:00:00Z"}`))
		}, true},
		{"отправка без времени", func() error {
			return h.Logistics(ctx, msg("TransportDispatched", `{"obligation_id":"01920000-0000-7000-8000-000000000001"}`))
		}, true},
		{"отрицательный защищённый объём", func() error {
			return h.Inventory(ctx, msg("ProtectedVolumePublished", `{"clinic_id":1,"component_kind":"RBC","volume_units":-1,"version":1,"reason":"x"}`))
		}, true},
	}
	for _, c := range cases {
		if err := c.run(); err == nil || !messaging.IsPermanent(err) {
			t.Errorf("%s: ожидали постоянную ошибку, получили %v", c.name, err)
		}
	}
	for name, run := range map[string]func() error{
		"известный чужой тип":     func() error { return h.Inventory(ctx, msg("TransferConfirmed", `{}`)) },
		"команда шлюзу в ответах": func() error { return h.Logistics(ctx, msg("RequestTransport", `{}`)) },
	} {
		if err := run(); err != nil {
			t.Errorf("%s: ожидали пропуск, получили %v", name, err)
		}
	}
}

func TestOpenObligationMapping(t *testing.T) {
	minExp := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	group := "A"
	c := transfer.OpenObligation{
		ChoiceID: uuid.New(), RequestID: uuid.New(), Origin: transfer.OriginDoctorChoice, SourceClinicID: 1, TargetClinicID: 2,
		ComponentKind: "RBC", RequestedQty: 3, Urgency: transfer.UrgencyEmergency, ConfirmWindowSeconds: 900,
		FallbackSources: []int64{4, 5},
		Spec:            transfer.ReplacementSpec{BloodGroup: &group, MinExpiresAt: &minExp, RuleVersion: "O15-v3"},
	}
	n := kafka.OpenObligationInput(c)
	if n.ConfirmWindow != 15*time.Minute || n.Origin != domain.OriginDoctorChoice || n.UrgencyCode != "EMERGENCY" {
		t.Errorf("перевод команды: %+v", n)
	}
	if n.Spec.Phenotype != nil {
		t.Error("пустой фенотип в контракте — нет требования (nil)")
	}
	if err := n.Validate(); err != nil {
		t.Errorf("корректная команда не прошла проверку: %v", err)
	}
	c.Spec.Phenotype = "CcDee"
	if p := kafka.OpenObligationInput(c).Spec.Phenotype; p == nil || *p != "CcDee" {
		t.Error("фенотип спецификации потерян")
	}
}

func TestUnitRegisteredMapping(t *testing.T) {
	e := inventory.UnitRegistered{UnitID: uuid.New(), ClinicID: 1, ComponentKind: "RBC", BloodGroup: "A", RhPositive: true,
		VolumeML: 300, ExpiresAt: time.Now(), Status: inventory.StatusQuarantine, Version: 7}
	in := kafka.UnitRegisteredInput(e)
	if in.Status != domain.UnitQuarantine || in.Version != 7 || in.Phenotype != nil {
		t.Errorf("перевод регистрации: %+v", in)
	}
}
