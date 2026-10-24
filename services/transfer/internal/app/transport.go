package app

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

var courierPattern = regexp.MustCompile(logistics.CourierRefPattern)
var transportRefPattern = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,128}$`)

func (s *Service) Transport(ctx context.Context, id uuid.UUID) (TransportRecord, error) {
	return s.store.Transport(ctx, s.pool, id)
}
func (s *Service) Escalations(ctx context.Context, includeClosed bool) ([]EscalationRecord, error) {
	return s.store.Escalations(ctx, s.pool, includeClosed, 200, nil)
}
func (s *Service) All(ctx context.Context, status *domain.Status, offset int) ([]ObligationView, error) {
	if offset < 0 {
		return nil, ErrInvalidEvent
	}
	items, err := s.store.AllObligations(ctx, s.pool, status, 100, offset)
	if err != nil {
		return nil, err
	}
	return s.views(ctx, items)
}

func (s *Service) ManualTransport(ctx context.Context, id uuid.UUID, reference, courier, actor string) error {
	if !transportRefPattern.MatchString(reference) || !courierPattern.MatchString(courier) {
		return domain.ErrInvalidTransport
	}
	return s.inTx(ctx, nil, func(ctx context.Context, tx pgx.Tx) error {
		o, err := s.store.LockObligation(ctx, tx, id)
		if err != nil {
			return err
		}
		if o.Status == domain.StatusTransportRequested {
			old, err := s.store.Transport(ctx, tx, id)
			if err == nil && old.Manual && old.TransportRef == reference && old.CourierRef == courier {
				return nil
			}
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			return ErrConflict
		}
		if o.Status != domain.StatusConfirmed {
			return ErrConflict
		}
		changed, err := s.store.CASTransportRequested(ctx, tx, id, reference, s.clock.Now())
		if err != nil {
			return err
		}
		if !changed {
			return ErrConflict
		}
		n := TransportRecord{ObligationID: id, TransportRef: reference, CourierRef: courier, Manual: true, AssignedBy: actor, AssignedAt: s.clock.Now(), Violations: []string{}}
		if err := s.store.SaveTransport(ctx, tx, n); err != nil {
			return err
		}
		if err := s.command(ctx, tx, id, logistics.CancelTransport{ObligationID: id, Reason: logistics.CancelObligationCancelled}); err != nil {
			return err
		}
		refs := refsAt(o, o.Version+1)
		if err := s.event(ctx, tx, id, transfer.ManualTransportAssigned{ObligationID: id, ObligationVersion: refs.version, SourceClinicID: refs.source, TargetClinicID: refs.target, RequestID: o.RequestID, TransportRef: reference, AssignedBy: actor, At: s.clock.Now()}); err != nil {
			return err
		}
		if err := s.event(ctx, tx, id, transfer.TransportRequested{ObligationID: id, ObligationVersion: refs.version, SourceClinicID: refs.source, TargetClinicID: refs.target, ShipmentRef: reference}); err != nil {
			return err
		}
		open, err := s.store.Escalations(ctx, tx, false, 100, &id)
		if err != nil {
			return err
		}
		for _, e := range open {
			if e.Reason != transfer.EscalationTransportRefused && e.Reason != transfer.EscalationTransportOutcomeUnknown {
				continue
			}
			if err := s.closeEscalationTx(ctx, tx, e.ID, actor, transfer.ResolutionManualTransport, "Назначена ручная перевозка"); err != nil {
				return err
			}
		}
		return s.audit(ctx, tx, actor, "MANUAL_TRANSPORT_ASSIGNED", o, map[string]any{"transport_ref": reference, "courier_ref": courier}, "DUTY_OFFICER")
	})
}

func (s *Service) CloseEscalation(ctx context.Context, id uuid.UUID, actor, resolution, comment string) error {
	comment = strings.TrimSpace(comment)
	if (resolution != transfer.ResolutionResolved && resolution != transfer.ResolutionNoActionRequired) || comment == "" || len(comment) > 1000 {
		return domain.ErrInvalidTransport
	}
	return s.inTx(ctx, nil, func(ctx context.Context, tx pgx.Tx) error {
		return s.closeEscalationTx(ctx, tx, id, actor, resolution, comment)
	})
}
func (s *Service) closeEscalationTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, actor, resolution, comment string) error {
	e, changed, err := s.store.CloseEscalation(ctx, tx, id, actor, resolution, comment, s.clock.Now())
	if err != nil || !changed {
		return err
	}
	o, err := s.store.Obligation(ctx, tx, e.ObligationID)
	if err != nil {
		return err
	}
	refs := refsAt(o, o.Version)
	if err := s.event(ctx, tx, o.ID, transfer.EscalationClosed{ObligationID: o.ID, ObligationVersion: refs.version, SourceClinicID: refs.source, TargetClinicID: refs.target, RequestID: o.RequestID, Reason: e.Reason, Resolution: resolution, ClosedBy: actor, At: s.clock.Now()}); err != nil {
		return err
	}
	return s.audit(ctx, tx, actor, "ESCALATION_CLOSED", o, map[string]any{"escalation_id": id.String(), "resolution": resolution, "comment": comment}, "DUTY_OFFICER")
}

func (s *Service) Dispatch(ctx context.Context, msg *messaging.Message, c DispatchInput) error {
	if !courierPattern.MatchString(c.CourierRef) || (msg == nil && !c.ContainerIntact) || !validTemperature(c.Temperature) {
		return domain.ErrInvalidTransport
	}
	if c.At.IsZero() {
		c.At = s.clock.Now()
	}
	if c.At.After(s.clock.Now().Add(time.Minute)) {
		return domain.ErrInvalidTransport
	}
	return s.inTx(ctx, msg, func(ctx context.Context, tx pgx.Tx) error {
		before, err := s.store.Obligation(ctx, tx, c.ObligationID)
		if err != nil {
			return err
		}
		if _, err := s.store.LockStock(ctx, tx, before.SourceClinicID, before.ComponentKind); err != nil {
			return err
		}
		o, err := s.store.LockObligation(ctx, tx, before.ID)
		if err != nil {
			return err
		}
		if c.TransportRef != "" && c.TransportRef != o.TransportRef {
			return s.audit(ctx, tx, c.Actor, "TRANSPORT_REPLY_IGNORED", o, nil)
		}
		if msg != nil && c.TransportRef == "" {
			current, err := s.store.Transport(ctx, tx, o.ID)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			if current.Manual {
				return s.audit(ctx, tx, c.Actor, "TRANSPORT_REPLY_IGNORED", o, nil)
			}
		}
		if o.Status == domain.StatusDispatched || o.Status == domain.StatusDelivered {
			old, err := s.store.Transport(ctx, tx, o.ID)
			if err != nil {
				return err
			}
			if old.CourierRef != c.CourierRef || !sameTemperature(old.DepartureTemperature, c.Temperature) {
				return ErrConflict
			}
			return nil
		}
		if o.Status != domain.StatusTransportRequested {
			return ErrConflict
		}
		if c.At.Before(o.CreatedAt) {
			return domain.ErrInvalidTransport
		}
		violations, err := domain.EvaluateTransport(o.ComponentKind, domain.TransportConditions{DepartedAt: c.At, ArrivedAt: c.At, DepartureTemperature: c.Temperature, DepartureContainerIntact: c.ContainerIntact, ArrivalContainerIntact: true})
		if err != nil || (msg == nil && len(violations) > 0) {
			return domain.ErrInvalidTransport
		}
		units, err := s.store.ConfirmedUnits(ctx, tx, o.ID)
		if err != nil {
			return err
		}
		if len(units) != o.ConfirmedQty || len(units) == 0 {
			return ErrConflict
		}
		copies, err := s.store.UnitCopies(ctx, tx, units)
		if err != nil {
			return err
		}
		for _, id := range units {
			u, ok := copies[id]
			if !ok || !u.Status.Allocatable() || !u.ExpiresAt.After(s.clock.Now()) {
				return ErrConflict
			}
		}
		used, err := s.store.DispatchUnits(ctx, tx, o.ID, units, s.clock.Now())
		if err != nil {
			return err
		}
		if err := s.store.AddReserved(ctx, tx, o.SourceClinicID, o.ComponentKind, -used); err != nil {
			return err
		}
		if err := s.store.RecountAvailable(ctx, tx, o.SourceClinicID, o.ComponentKind); err != nil {
			return err
		}
		changed, err := s.store.CASStatus(ctx, tx, o, domain.StatusDispatched, "DISPATCHED", s.clock.Now())
		if err != nil {
			return err
		}
		if !changed {
			return ErrConflict
		}
		n, err := s.store.Transport(ctx, tx, o.ID)
		if errors.Is(err, ErrNotFound) {
			n = TransportRecord{ObligationID: o.ID, TransportRef: o.TransportRef, AssignedBy: c.Actor, AssignedAt: s.clock.Now(), Violations: []string{}}
		} else if err != nil {
			return err
		}
		n.CourierRef = c.CourierRef
		n.DepartedAt = &c.At
		n.DepartureTemperature = c.Temperature
		n.DepartureContainerIntact = c.ContainerIntact
		n.Violations = violations
		n.ConditionsViolated = len(violations) > 0
		if n.ConditionsViolated {
			if err := s.store.BlockTransportUnits(ctx, tx, o.ID); err != nil {
				return err
			}
		}
		if err := s.store.SaveTransport(ctx, tx, n); err != nil {
			return err
		}
		refs := refsAt(o, o.Version+1)
		if err := s.event(ctx, tx, o.ID, transfer.TransferDispatched{ObligationID: o.ID, ObligationVersion: refs.version, SourceClinicID: refs.source, TargetClinicID: refs.target, RequestID: o.RequestID, At: c.At}); err != nil {
			return err
		}
		if n.ConditionsViolated {
			if err := s.event(ctx, tx, o.ID, transfer.EscalationRaised{ObligationID: o.ID, ObligationVersion: refs.version, SourceClinicID: refs.source, TargetClinicID: refs.target, RequestID: o.RequestID, Reason: transfer.EscalationTemperatureExcursion, At: s.clock.Now()}); err != nil {
				return err
			}
		}
		return s.audit(ctx, tx, c.Actor, "DISPATCHED", o, map[string]any{"courier_ref": c.CourierRef, "units": units, "departed_at": c.At}, c.Role)
	})
}

func (s *Service) Receive(ctx context.Context, msg *messaging.Message, c ReceiveInput) error {
	if !validTemperature(c.Temperature) || !validTemperature(c.DepartureTemperature) {
		return domain.ErrInvalidTransport
	}
	if c.At.IsZero() {
		c.At = s.clock.Now()
	}
	if c.At.After(s.clock.Now().Add(time.Minute)) {
		return domain.ErrInvalidTransport
	}
	return s.inTx(ctx, msg, func(ctx context.Context, tx pgx.Tx) error {
		before, err := s.store.Obligation(ctx, tx, c.ObligationID)
		if err != nil {
			return err
		}
		if _, err := s.store.LockStock(ctx, tx, before.SourceClinicID, before.ComponentKind); err != nil {
			return err
		}
		o, err := s.store.LockObligation(ctx, tx, before.ID)
		if err != nil {
			return err
		}
		if c.TransportRef != "" && c.TransportRef != o.TransportRef {
			return s.audit(ctx, tx, c.Actor, "TRANSPORT_REPLY_IGNORED", o, nil)
		}
		n, err := s.store.Transport(ctx, tx, o.ID)
		if err != nil {
			return err
		}
		if msg != nil && n.Manual && c.TransportRef == "" {
			return s.audit(ctx, tx, c.Actor, "TRANSPORT_REPLY_IGNORED", o, nil)
		}
		if o.Status == domain.StatusDelivered {
			if n.ArrivalContainerIntact != c.ContainerIntact || !sameTemperature(n.ArrivalTemperature, c.Temperature) || n.TemperatureExcursion != c.TemperatureExcursion {
				return ErrConflict
			}
			return nil
		}
		if o.Status != domain.StatusDispatched || n.DepartedAt == nil {
			return ErrConflict
		}
		if n.DepartureTemperature == nil {
			n.DepartureTemperature = c.DepartureTemperature
		} else if c.DepartureTemperature != nil && *n.DepartureTemperature != *c.DepartureTemperature {
			c.TemperatureExcursion = true
		}
		violations, err := domain.EvaluateTransport(o.ComponentKind, domain.TransportConditions{DepartedAt: *n.DepartedAt, ArrivedAt: c.At, DepartureTemperature: n.DepartureTemperature, ArrivalTemperature: c.Temperature, DepartureContainerIntact: n.DepartureContainerIntact, ArrivalContainerIntact: c.ContainerIntact, TemperatureExcursion: c.TemperatureExcursion})
		if err != nil {
			return err
		}
		n.ArrivedAt = &c.At
		n.ArrivalTemperature = c.Temperature
		n.ArrivalContainerIntact = c.ContainerIntact
		n.TemperatureExcursion = c.TemperatureExcursion
		n.Violations = violations
		n.ConditionsViolated = len(violations) > 0
		if err := s.store.SaveTransport(ctx, tx, n); err != nil {
			return err
		}
		if n.ConditionsViolated {
			if err := s.store.BlockTransportUnits(ctx, tx, o.ID); err != nil {
				return err
			}
		}
		changed, err := s.store.CASStatus(ctx, tx, o, domain.StatusDelivered, "DELIVERED", s.clock.Now())
		if err != nil {
			return err
		}
		if !changed {
			return ErrConflict
		}
		refs := refsAt(o, o.Version+1)
		if err := s.event(ctx, tx, o.ID, transfer.TransferDelivered{ObligationID: o.ID, ObligationVersion: refs.version, SourceClinicID: refs.source, TargetClinicID: refs.target, RequestID: o.RequestID, At: c.At, ConditionsViolated: &n.ConditionsViolated, Violations: violations}); err != nil {
			return err
		}
		if n.ConditionsViolated {
			if err := s.event(ctx, tx, o.ID, transfer.EscalationRaised{ObligationID: o.ID, ObligationVersion: refs.version, SourceClinicID: refs.source, TargetClinicID: refs.target, RequestID: o.RequestID, Reason: transfer.EscalationTemperatureExcursion, At: s.clock.Now()}); err != nil {
				return err
			}
		}
		return s.audit(ctx, tx, c.Actor, "DELIVERED", o, map[string]any{"conditions_violated": n.ConditionsViolated, "violations": violations, "arrived_at": c.At}, c.Role)
	})
}

func validTemperature(value *float64) bool {
	return value == nil || (!math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= -100 && *value <= 100)
}

func sameTemperature(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
