// Package kafka — граница шлюза с шиной (ADR-0003): обработчик logistics.commands и ответы в logistics.replies
// через outbox. Сам Kafka-клиент живёт в libs/messaging; здесь только контракты libs/contracts/logistics.
package kafka

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/domain"
)

// ErrUnknownRegime — режим перевозки не задан и не выводится из вида компонента, или неизвестен.
var ErrUnknownRegime = errors.New("неизвестный температурный режим перевозки")

var knownRegimes = map[string]bool{
	logistics.RegimeCold2to6:      true,
	logistics.RegimeFrozenMinus25: true,
	logistics.RegimeRoom20to24:    true,
}

// RequestFrom переводит команду в заказ шлюза. Нет temperature_regime — режим выводится из вида компонента
// (демо, ADR-0010: RBC 2–6 °C, FFP и CRYO −25 °C, PLT 20–24 °C); условия транспортировки — ПП РФ № 641.
func RequestFrom(c logistics.RequestTransport) (domain.Request, error) {
	regime := c.TemperatureRegime
	if regime == "" {
		r, ok := logistics.TemperatureRegimeFor(c.ComponentKind)
		if !ok {
			return domain.Request{}, fmt.Errorf("%w: вид компонента %q", ErrUnknownRegime, c.ComponentKind)
		}
		regime = r
	}
	if !knownRegimes[regime] {
		return domain.Request{}, fmt.Errorf("%w: %q", ErrUnknownRegime, regime)
	}
	r := domain.Request{
		ObligationID: c.ObligationID, RequestID: c.RequestID, SourceClinicID: c.SourceClinicID,
		TargetClinicID: c.TargetClinicID, ComponentKindCode: c.ComponentKind, Quantity: c.Quantity,
		TemperatureRegime: regime,
	}
	if err := r.Validate(); err != nil {
		return domain.Request{}, err
	}
	return r, nil
}

// Handler — обработчик logistics.commands. Битая или неизвестная команда — messaging.Permanent (сразу в DLT):
// повтор её не исправит.
func Handler(g *app.Gateway) messaging.Handler {
	return func(ctx context.Context, msg messaging.Message) error {
		switch msg.Type {
		case logistics.RequestTransport{}.MessageType():
			var c logistics.RequestTransport
			if err := msg.Decode(&c); err != nil {
				return messaging.Permanent(err)
			}
			req, err := RequestFrom(c)
			if err != nil {
				return messaging.Permanent(err)
			}
			return g.AcceptRequest(ctx, msg, req)
		case logistics.CancelTransport{}.MessageType():
			var c logistics.CancelTransport
			if err := msg.Decode(&c); err != nil {
				return messaging.Permanent(err)
			}
			if c.ObligationID == uuid.Nil {
				return messaging.Permanent(errors.New("CancelTransport без obligation_id"))
			}
			return g.AcceptCancel(ctx, msg, c.ObligationID)
		default:
			return messaging.Permanent(fmt.Errorf("неизвестная команда в %s: %s", msg.Topic, msg.Type))
		}
	}
}

// Outbox пишет ответы transfer в logistics.replies (ключ — obligation_id) и аудит через Journal.
type Outbox struct {
	Journal *messaging.Journal
	// Topic — топик ответов; в рабочем сервисе contracts.TopicLogisticsReplies, в тестах уникальный.
	Topic string
}

// Reply реализует app.Outbox.
func (o Outbox) Reply(ctx context.Context, tx pgx.Tx, r domain.Reply) error {
	var p contracts.Payload
	switch r.Kind {
	case domain.ReplyAccepted:
		p = logistics.TransportAccepted{ObligationID: r.ObligationID, ShipmentRef: r.ShipmentRef}
	case domain.ReplyRejected:
		p = logistics.TransportRejected{ObligationID: r.ObligationID, Reason: r.Reason, Details: r.Details}
	case domain.ReplyCancelled:
		p = logistics.TransportCancelled{ObligationID: r.ObligationID}
	default:
		return fmt.Errorf("неизвестный ответ шлюза %q", r.Kind)
	}
	if _, err := o.Journal.Publish(ctx, tx, o.Topic, r.ObligationID.String(), p); err != nil {
		return fmt.Errorf("ответ %s в outbox: %w", r.Kind, err)
	}
	return nil
}

// Audit реализует app.Outbox.
func (o Outbox) Audit(ctx context.Context, tx pgx.Tx, e messaging.AuditEntry) error {
	if err := o.Journal.Audit(ctx, tx, e); err != nil {
		return fmt.Errorf("аудит шлюза: %w", err)
	}
	return nil
}

var _ app.Outbox = Outbox{}
