package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
)

// ShipmentProgress — факты перевозки C03. Отсутствующие измерения не подменяются нормой.
type ShipmentProgress struct {
	DispatchedAt             *time.Time `json:"dispatchedAt,omitempty"`
	DeliveredAt              *time.Time `json:"deliveredAt,omitempty"`
	CourierRef               string     `json:"courierRef,omitempty"`
	DepartureTemperature     *float64   `json:"temperatureAtDispatchC,omitempty"`
	ArrivalTemperature       *float64   `json:"temperatureAtArrivalC,omitempty"`
	DepartureContainerIntact *bool      `json:"departureContainerIntact,omitempty"`
	ArrivalContainerIntact   *bool      `json:"arrivalContainerIntact,omitempty"`
	TemperatureExcursion     *bool      `json:"temperatureExcursion,omitempty"`
}
type ProgressOrder struct {
	ID          uuid.UUID
	ShipmentRef string
}

type TransportStatus struct {
	ID                                 uuid.UUID
	Status, ShipmentRef, LastError     string
	UpdatedAt                          time.Time
	DispatchReported, DeliveryReported bool
}
type ProgressSource interface {
	Progress(context.Context, ProgressOrder) (ShipmentProgress, error)
}
type ProgressStore interface {
	ProgressDue(context.Context, time.Time, int) ([]ProgressOrder, error)
	SaveProgress(context.Context, ProgressOrder, ShipmentProgress) error
}
type ProgressFeed struct {
	Store  ProgressStore
	Source ProgressSource
	Clock  clock.Clock
	Log    *slog.Logger
}

func (f ProgressFeed) Run(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := f.Poll(ctx); err != nil && ctx.Err() == nil {
			f.Log.ErrorContext(ctx, "сверка фактов перевозки не выполнена", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (f ProgressFeed) Poll(ctx context.Context) error {
	rows, err := f.Store.ProgressDue(ctx, f.Clock.Now(), 100)
	if err != nil {
		return err
	}
	for _, row := range rows {
		progress, err := f.Source.Progress(ctx, row)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			f.Log.WarnContext(ctx, "C03 не передала факты перевозки", "obligation_id", row.ID, "err", err)
			continue
		}
		if err := f.Store.SaveProgress(ctx, row, progress); err != nil {
			return err
		}
	}
	return nil
}
