package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/int02"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/domain"
)

var ErrNotFound = errors.New("объект планирования не найден")
var ErrInvalid = errors.New("неверный запрос планирования")
var ErrConflict = errors.New("версия сценария уже загружена с другим содержимым")

type Projection struct {
	Key     string
	Version int64
	At      time.Time
	Payload json.RawMessage
}
type PlanRecord struct {
	Plan      *domain.Plan
	ElapsedMS int64
}

type ForecastRecord struct {
	ID         uuid.UUID
	ReceivedAt time.Time
	Forecast   int02.EmergencyForecast
}

type DeficitRecord struct {
	ID       uuid.UUID `json:"deficit_record_id"`
	Type     string    `json:"record_type"`
	OriginID uuid.UUID `json:"origin_id"`
	ClinicID int64     `json:"clinic_id"`
	Kind     string    `json:"component_kind"`
	Group    string    `json:"blood_group"`
	ML       int64     `json:"uncovered_ml"`
	Units    int       `json:"uncovered_units"`
	Reason   string    `json:"reason"`
	At       time.Time `json:"created_at"`
}
type Store interface {
	RecordDeficit(context.Context, pgx.Tx, DeficitRecord) error
	Deficits(context.Context, int64, string, string) ([]DeficitRecord, error)
	SupersedePlans(context.Context, pgx.Tx, uuid.UUID) ([]uuid.UUID, error)
	Project(context.Context, pgx.Tx, Projection) error
	Projection(context.Context, pgx.Tx, string) (json.RawMessage, error)
	Inputs(context.Context, pgx.Tx) ([]Projection, error)
	InsertForecast(context.Context, pgx.Tx, uuid.UUID, int02.EmergencyForecast, time.Time) (bool, error)
	Forecast(context.Context, pgx.Tx, uuid.UUID) (int02.EmergencyForecast, error)
	Forecasts(context.Context) ([]ForecastRecord, error)
	LockBuild(context.Context, pgx.Tx) error
	NextVersion(context.Context, pgx.Tx) (int64, error)
	SavePlan(context.Context, pgx.Tx, domain.Input, PlanRecord) error
	ReadPlan(context.Context, pgx.Tx, uuid.UUID) (PlanRecord, error)
	Plans(context.Context) ([]PlanRecord, error)
}

type unitInput struct {
	ID      uuid.UUID  `json:"unit_id"`
	Clinic  int64      `json:"clinic_id,omitempty"`
	Kind    string     `json:"component_kind,omitempty"`
	Group   string     `json:"blood_group,omitempty"`
	Volume  int64      `json:"volume_ml,omitempty"`
	Expires *time.Time `json:"expires_at,omitempty"`
	Status  string     `json:"status"`
}
type obligationInput struct {
	ConditionsViolated *bool       `json:"conditions_violated,omitempty"`
	ID                 uuid.UUID   `json:"obligation_id"`
	Source             int64       `json:"source_clinic_id,omitempty"`
	Target             int64       `json:"target_clinic_id,omitempty"`
	Kind               string      `json:"component_kind,omitempty"`
	Status             string      `json:"status"`
	Quantity           int         `json:"quantity,omitempty"`
	Units              []uuid.UUID `json:"units,omitempty"`
}
