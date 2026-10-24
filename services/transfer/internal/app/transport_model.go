package app

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrConflict = errors.New("состояние задания изменилось")

type TransportRecord struct {
	TemperatureExcursion     bool       `json:"temperature_excursion"`
	ObligationID             uuid.UUID  `json:"obligation_id"`
	TransportRef             string     `json:"transport_ref"`
	CourierRef               string     `json:"courier_ref"`
	Manual                   bool       `json:"manual"`
	AssignedBy               string     `json:"assigned_by"`
	AssignedAt               time.Time  `json:"assigned_at"`
	DepartedAt               *time.Time `json:"departed_at"`
	ArrivedAt                *time.Time `json:"arrived_at"`
	DepartureTemperature     *float64   `json:"temperature_at_dispatch_c"`
	ArrivalTemperature       *float64   `json:"temperature_at_arrival_c"`
	DepartureContainerIntact bool       `json:"departure_container_intact"`
	ArrivalContainerIntact   bool       `json:"arrival_container_intact"`
	ConditionsViolated       bool       `json:"conditions_violated"`
	Violations               []string   `json:"violations"`
}
type EscalationRecord struct {
	ID                            uuid.UUID
	ObligationID                  uuid.UUID
	Reason                        string
	RaisedAt                      time.Time
	ClosedAt                      *time.Time
	ClosedBy, Resolution, Comment string
}
type DispatchInput struct {
	Role                            string
	ObligationID                    uuid.UUID
	CourierRef, TransportRef, Actor string
	At                              time.Time
	Temperature                     *float64
	ContainerIntact                 bool
}
type ReceiveInput struct {
	Role                                  string
	TransportRef                          string
	DepartureTemperature                  *float64
	ObligationID                          uuid.UUID
	Actor                                 string
	At                                    time.Time
	Temperature                           *float64
	ContainerIntact, TemperatureExcursion bool
}
