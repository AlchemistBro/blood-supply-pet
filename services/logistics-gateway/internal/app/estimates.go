package app

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
)

type EstimateSource interface {
	Estimates(context.Context, int64) (logistics.DeliveryEstimatesPublished, error)
}

type Estimates struct {
	Pool    *pgxpool.Pool
	Source  EstimateSource
	Journal *messaging.Journal
	Ref     *refdata.Set
	Log     *slog.Logger
}

// Refresh обращается к C03 вне транзакции. Частичный сбой не публикует неполный набор.
func (e Estimates) Refresh(ctx context.Context, sources []int64, msg *messaging.Message) error {
	if len(sources) == 0 {
		for _, clinic := range e.Ref.Clinics {
			if clinic.IsActive {
				sources = append(sources, clinic.ClinicID)
			}
		}
	}
	if len(sources) > 1000 {
		return messaging.Permanent(fmt.Errorf("слишком много источников оценки"))
	}
	events := make([]logistics.DeliveryEstimatesPublished, 0, len(sources))
	for _, id := range sources {
		if _, ok := e.Ref.Clinic(id); !ok {
			return messaging.Permanent(fmt.Errorf("неизвестный источник оценки"))
		}
		event, err := e.Source.Estimates(ctx, id)
		if err != nil {
			return err
		}
		for _, route := range event.Estimates {
			if _, ok := e.Ref.Clinic(route.TargetClinicID); !ok {
				return fmt.Errorf("C03 вернула неизвестную клинику маршрута")
			}
		}
		events = append(events, event)
	}
	return bspgx.WithTx(ctx, e.Pool, func(ctx context.Context, tx pgx.Tx) error {
		if msg != nil {
			first, err := messaging.FirstTime(ctx, tx, *msg, msg.OccurredAt)
			if err != nil || !first {
				return err
			}
		}
		for _, event := range events {
			if _, err := e.Journal.Publish(ctx, tx, contracts.TopicLogisticsEvents, strconv.FormatInt(event.SourceClinicID, 10), event); err != nil {
				return err
			}
		}
		return nil
	})
}

func (e Estimates) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := e.Refresh(ctx, nil, nil); err != nil && ctx.Err() == nil {
			e.Log.WarnContext(ctx, "не удалось обновить оценки доставки", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (e Estimates) Handle(next messaging.Handler) messaging.Handler {
	return func(ctx context.Context, msg messaging.Message) error {
		if msg.Type != "RefreshDeliveryEstimates" {
			return next(ctx, msg)
		}
		var command logistics.RefreshDeliveryEstimates
		if err := msg.Decode(&command); err != nil {
			return messaging.Permanent(err)
		}
		return e.Refresh(ctx, command.SourceClinicIDs, &msg)
	}
}
