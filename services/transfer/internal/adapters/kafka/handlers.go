// Package kafka — обработчики входящих топиков transfer: transfer.commands, inventory.events, logistics.replies.
// Здесь контракт из libs/contracts переводится во вход сценария app; транзакция, inbox (FirstTime) и работа —
// в app одной транзакцией. Неразбираемое сообщение и неизвестный тип — постоянная ошибка (DLT).
package kafka

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// Handlers — обработчики для messaging.NewConsumer.
type Handlers struct {
	svc *app.Service
	log *slog.Logger
}

// New создаёт обработчики.
func New(svc *app.Service, log *slog.Logger) *Handlers {
	return &Handlers{svc: svc, log: log}
}

// Commands — transfer.commands: OpenObligation, CancelObligation.
func (h *Handlers) Commands(ctx context.Context, msg messaging.Message) error {
	switch msg.Type {
	case (transfer.CancelRequestObligations{}).MessageType():
		var c transfer.CancelRequestObligations
		if err := decode(msg, &c); err != nil {
			return err
		}
		return h.svc.CancelRequest(ctx, &msg, c)
	case (transfer.OpenObligation{}).MessageType():
		var c transfer.OpenObligation
		if err := decode(msg, &c); err != nil {
			return err
		}
		_, err := h.svc.Open(ctx, &msg, OpenObligationInput(c))
		return err
	case (transfer.CancelObligation{}).MessageType():
		var c transfer.CancelObligation
		if err := decode(msg, &c); err != nil {
			return err
		}
		_, err := h.svc.Cancel(ctx, &msg, app.CancelObligation{ObligationID: c.ObligationID, Reason: c.Reason, Actor: msg.Producer})
		return err
	default:
		return h.foreign(ctx, msg)
	}
}

// Inventory — inventory.events: проекция единиц и защищённого объёма.
func (h *Handlers) Inventory(ctx context.Context, msg messaging.Message) error {
	switch msg.Type {
	case (inventory.UnitRegistered{}).MessageType():
		var e inventory.UnitRegistered
		if err := decode(msg, &e); err != nil {
			return err
		}
		return h.svc.OnUnitRegistered(ctx, &msg, UnitRegisteredInput(e))
	case (inventory.UnitUpdated{}).MessageType():
		var e inventory.UnitUpdated
		if err := decode(msg, &e); err != nil {
			return err
		}
		return h.svc.OnUnitUpdated(ctx, &msg, app.UnitUpdated{
			UnitID: e.UnitID, ClinicID: e.ClinicID, ComponentKind: e.ComponentKind,
			Status: domain.UnitStatus(e.Status), Version: e.Version,
		})
	case (inventory.UnitWithdrawn{}).MessageType():
		var e inventory.UnitWithdrawn
		if err := decode(msg, &e); err != nil {
			return err
		}
		return h.svc.OnUnitWithdrawn(ctx, &msg, app.UnitWithdrawn{
			UnitID: e.UnitID, ClinicID: e.ClinicID, ComponentKind: e.ComponentKind, Reason: e.Reason, Version: e.Version,
		})
	case (inventory.ProtectedVolumePublished{}).MessageType():
		var e inventory.ProtectedVolumePublished
		if err := decode(msg, &e); err != nil {
			return err
		}
		if e.VolumeUnits < 0 || e.VolumeUnits > math.MaxInt32 {
			return messaging.Permanent(fmt.Errorf("%w: volume_units %d вне диапазона", app.ErrInvalidEvent, e.VolumeUnits))
		}
		return h.svc.OnProtectedVolume(ctx, &msg, app.ProtectedVolume{
			ClinicID: e.ClinicID, ComponentKind: e.ComponentKind, VolumeUnits: int(e.VolumeUnits), Version: e.Version, Reason: e.Reason,
		})
	default:
		return h.foreign(ctx, msg)
	}
}

// Logistics — logistics.replies: ответы шлюза перевозчика.
func (h *Handlers) Logistics(ctx context.Context, msg messaging.Message) error {
	switch msg.Type {
	case (logistics.TransportAccepted{}).MessageType():
		var r logistics.TransportAccepted
		if err := decode(msg, &r); err != nil {
			return err
		}
		return h.svc.OnLogisticsReply(ctx, &msg, app.LogisticsReply{Kind: app.ReplyAccepted, ObligationID: r.ObligationID, ShipmentRef: r.ShipmentRef})
	case (logistics.TransportRejected{}).MessageType():
		var r logistics.TransportRejected
		if err := decode(msg, &r); err != nil {
			return err
		}
		return h.svc.OnLogisticsReply(ctx, &msg, app.LogisticsReply{
			Kind: app.ReplyRejected, ObligationID: r.ObligationID, Reason: r.Reason, Details: r.Details,
		})
	case (logistics.TransportCancelled{}).MessageType():
		var r logistics.TransportCancelled
		if err := decode(msg, &r); err != nil {
			return err
		}
		return h.svc.OnLogisticsReply(ctx, &msg, app.LogisticsReply{Kind: app.ReplyCancelled, ObligationID: r.ObligationID})
	case (logistics.TransportDelivered{}).MessageType():
		// Статусы DISPATCHED и DELIVERED появляются на этапе 7. До тех пор доставка только отмечается в логе:
		// постоянная ошибка отправила бы в DLT каждую штатную доставку и подняла бы ложный алерт.
		h.log.InfoContext(ctx, "TransportDelivered пропущен: статусы доставки появятся на этапе 7", "message_id", msg.ID)
		return nil
	default:
		return h.foreign(ctx, msg)
	}
}

// foreign — тип из реестра контрактов, который transfer не обрабатывает (например, команда шлюзу в том же
// топике) — пропускается; тип, которого нет в реестре, — ядовитое сообщение (DLT).
func (h *Handlers) foreign(ctx context.Context, msg messaging.Message) error {
	if _, known := contracts.Registry[msg.Type]; known {
		h.log.DebugContext(ctx, "сообщение не для transfer, пропущено", "type", msg.Type, "topic", msg.Topic)
		return nil
	}
	return messaging.Permanent(fmt.Errorf("неизвестный тип сообщения %q в %s", msg.Type, msg.Topic))
}

func decode(msg messaging.Message, v any) error {
	if err := msg.Decode(v); err != nil {
		return messaging.Permanent(err)
	}
	return nil
}

// OpenObligationInput переводит команду в открытие задания. Номера случая пациента в команде нет.
func OpenObligationInput(c transfer.OpenObligation) domain.NewObligation {
	return domain.NewObligation{
		ProtectedFloors: c.ProtectedFloors,
		ChoiceID:        c.ChoiceID, RequestID: c.RequestID, Origin: domain.Origin(c.Origin),
		SourceClinicID: c.SourceClinicID, TargetClinicID: c.TargetClinicID, ComponentKind: c.ComponentKind,
		RequestedQty: c.RequestedQty, UrgencyCode: c.Urgency,
		ConfirmWindow: time.Duration(c.ConfirmWindowSeconds) * time.Second, FallbackSources: c.FallbackSources,
		Spec: domain.ReplacementSpec{
			BloodGroup: c.Spec.BloodGroup, RhPositive: c.Spec.RhPositive, Phenotype: optional(c.Spec.Phenotype),
			MinExpiresAt: c.Spec.MinExpiresAt, RuleVersion: c.Spec.RuleVersion,
		},
	}
}

// UnitRegisteredInput переводит регистрацию единицы во вход проекции.
func UnitRegisteredInput(e inventory.UnitRegistered) app.UnitRegistered {
	return app.UnitRegistered{
		UnitID: e.UnitID, ClinicID: e.ClinicID, ComponentKind: e.ComponentKind, BloodGroup: e.BloodGroup,
		RhPositive: e.RhPositive, Phenotype: optional(e.Phenotype), VolumeML: e.VolumeML, ExpiresAt: e.ExpiresAt,
		Status: domain.UnitStatus(e.Status), Version: e.Version,
	}
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
