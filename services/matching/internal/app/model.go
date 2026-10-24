package app

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/k1seal"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/domain"
)

var (
	ErrNotFound  = errors.New("заявка или вариант не найдены")
	ErrForbidden = errors.New("нет доступа к заявке")
	ErrInvalid   = errors.New("неверный запрос подбора")
	ErrConflict  = errors.New("заявка уже изменена; обновите страницу")
)

type Actor struct {
	User     string
	Roles    []string
	ClinicID int64
}
type RequestRecord struct {
	ClinicalUseBlocked      bool
	Obligations             []RequestObligation
	PatientAllocationStatus string
	Request                 domain.Request
	CreatedBy               string
	Status                  string
	Result                  domain.Result
	Selection               *Selection
	Version                 int64
}
type RequestObligation struct {
	ID                 uuid.UUID
	Source             int64
	Status             string
	Units              int
	ClinicalUseBlocked bool
}
type Selection struct {
	VariantID, ChoiceID uuid.UUID
	By                  string
	At                  time.Time
}
type OutgoingChoice struct {
	ChoiceID uuid.UUID
	Source   int64
	Quantity int
}
type Projection struct {
	Key     string
	Version int64
	At      time.Time
	Payload json.RawMessage
}

type Store interface {
	InsertOutgoingChoice(context.Context, pgx.Tx, uuid.UUID, OutgoingChoice) error
	OutgoingChoices(context.Context, pgx.Tx, uuid.UUID) ([]OutgoingChoice, error)
	ClaimFollowUp(context.Context, pgx.Tx, uuid.UUID, int64, string) (bool, error)
	InsertRequest(context.Context, pgx.Tx, RequestRecord, k1seal.Sealed) error
	Request(context.Context, pgx.Tx, uuid.UUID, bool) (RequestRecord, error)
	Requests(context.Context, int64) ([]RequestRecord, error)
	Patient(context.Context, pgx.Tx, uuid.UUID) (k1seal.Sealed, error)
	Rules(context.Context, pgx.Tx) ([]domain.Rule, error)
	InsertRule(context.Context, pgx.Tx, domain.Rule) error
	CloseRule(context.Context, pgx.Tx, domain.Rule, time.Time) error
	SaveSelection(context.Context, pgx.Tx, uuid.UUID, Selection, string, time.Time) error
	SetStatus(context.Context, pgx.Tx, uuid.UUID, string, time.Time) error
	Project(context.Context, pgx.Tx, Projection) (bool, error)
	Projection(context.Context, pgx.Tx, string) (Projection, error)
	Inputs(context.Context, pgx.Tx) ([]Projection, error)
	RequestInputs(context.Context, pgx.Tx, uuid.UUID) ([]Projection, error)
}

func VariantID(request uuid.UUID, rank int) uuid.UUID {
	return uuid.NewSHA1(request, []byte(strconv.Itoa(rank)))
}
