package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/matching"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/domain"
)

type unitProjection struct {
	ID        uuid.UUID  `json:"unit_id"`
	Clinic    int64      `json:"clinic_id,omitempty"`
	Kind      string     `json:"component_kind,omitempty"`
	Group     string     `json:"blood_group,omitempty"`
	Phenotype string     `json:"phenotype,omitempty"`
	Volume    int64      `json:"volume_ml,omitempty"`
	Expires   *time.Time `json:"expires_at,omitempty"`
	Status    string     `json:"status"`
}
type obligationProjection struct {
	ConditionsViolated *bool       `json:"conditions_violated,omitempty"`
	RequestedQty       int         `json:"requested_qty,omitempty"`
	AllocationVersion  int64       `json:"allocation_version,omitempty"`
	ID                 uuid.UUID   `json:"obligation_id"`
	Request            uuid.UUID   `json:"request_id,omitempty"`
	Source             int64       `json:"source_clinic_id,omitempty"`
	Target             int64       `json:"target_clinic_id,omitempty"`
	Kind               string      `json:"component_kind,omitempty"`
	Status             string      `json:"status,omitempty"`
	Units              []uuid.UUID `json:"units,omitempty"`
	Version            int64       `json:"obligation_version,omitempty"`
	Next               *uuid.UUID  `json:"next_obligation_id,omitempty"`
}

func (s *Service) Handle(ctx context.Context, msg messaging.Message) error {
	if msg.Type == "TransferShortfallRecorded" {
		return s.shortfall(ctx, msg)
	}
	var key string
	var payload any
	version := int64(-1)
	switch msg.Type {
	case "PatientAllocationAcknowledged":
		var n inventory.PatientAllocationAcknowledged
		if err := msg.Decode(&n); err != nil {
			return messaging.Permanent(err)
		}
		if n.IdempotencyKey == uuid.Nil {
			return messaging.Permanent(fmt.Errorf("неверная квитанция ИНТ-05"))
		}
		key, version, payload = "allocation:"+n.IdempotencyKey.String(), 1, n
	case "UnitRegistered", "UnitUpdated", "UnitWithdrawn":
		var u unitProjection
		var meta struct {
			Version int64  `json:"version"`
			Reason  string `json:"reason"`
		}
		if err := msg.Decode(&u); err != nil {
			return messaging.Permanent(err)
		}
		if err := msg.Decode(&meta); err != nil {
			return messaging.Permanent(err)
		}
		if u.ID == uuid.Nil || meta.Version <= 0 {
			return messaging.Permanent(fmt.Errorf("неверная версия единицы"))
		}
		if msg.Type == "UnitWithdrawn" {
			u.Status = meta.Reason
		}
		key, version, payload = "unit:"+u.ID.String(), meta.Version, u
	case "ProtectedVolumePublished":
		var n inventory.ProtectedVolumePublished
		if err := msg.Decode(&n); err != nil {
			return messaging.Permanent(err)
		}
		key, version, payload = fmt.Sprintf("protected:%d:%s", n.ClinicID, n.ComponentKind), n.Version, n
	case "StockSnapshotPublished":
		var n inventory.StockSnapshotPublished
		if err := msg.Decode(&n); err != nil {
			return messaging.Permanent(err)
		}
		key, version, payload = fmt.Sprintf("stock:%d:%s:%s", n.ClinicID, n.ComponentKind, n.BloodGroup), n.Watermark, n
	case "DeliveryEstimatesPublished":
		var n logistics.DeliveryEstimatesPublished
		if err := msg.Decode(&n); err != nil {
			return messaging.Permanent(err)
		}
		key, version, payload = fmt.Sprintf("delivery:%d", n.SourceClinicID), n.EstimatedAt.UnixNano(), n
	case "ObligationOpened", "TransferConfirmed", "TransportRequested", "TransferRejected", "TransferExpired", "TransferCancelled", "TransferBroken", "TransferUnitReplaced", "TransferDispatched", "TransferDelivered":
		var n obligationProjection
		if err := msg.Decode(&n); err != nil {
			return messaging.Permanent(err)
		}
		if n.ID == uuid.Nil {
			return messaging.Permanent(fmt.Errorf("нет обязательства"))
		}
		n.Status = map[string]string{"ObligationOpened": "PENDING", "TransferConfirmed": "CONFIRMED", "TransportRequested": "TRANSPORT_REQUESTED", "TransferRejected": "REJECTED", "TransferExpired": "EXPIRED", "TransferCancelled": "CANCELLED", "TransferBroken": "BROKEN", "TransferDispatched": "DISPATCHED", "TransferDelivered": "DELIVERED"}[msg.Type]
		key, version, payload = "obligation:"+n.ID.String(), n.Version, n
		if msg.Type == "TransferConfirmed" || msg.Type == "TransferUnitReplaced" {
			n.AllocationVersion = n.Version
			payload = n
		}
	default:
		return nil
	}
	return bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		first, err := messaging.FirstTime(ctx, tx, msg, s.clock.Now())
		if err != nil || !first {
			return err
		}
		if msg.Type == "TransferUnitReplaced" {
			previous, err := s.store.Projection(ctx, tx, key)
			if err != nil {
				return err
			}
			var current obligationProjection
			if err := json.Unmarshal(previous.Payload, &current); err != nil {
				return err
			}
			var replacement struct {
				Old uuid.UUID `json:"old_unit_id"`
				New uuid.UUID `json:"new_unit_id"`
			}
			if err := msg.Decode(&replacement); err != nil {
				return messaging.Permanent(err)
			}
			for i, id := range current.Units {
				if id == replacement.Old {
					current.Units[i] = replacement.New
				}
			}
			payload = map[string]any{"units": current.Units, "allocation_version": version}
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		changed, err := s.store.Project(ctx, tx, Projection{Key: key, Version: version, At: msg.OccurredAt, Payload: raw})
		if err != nil || !changed {
			return err
		}
		if strings.HasPrefix(key, "obligation:") {
			projection, err := s.store.Projection(ctx, tx, key)
			if err != nil {
				return err
			}
			var o obligationProjection
			if err := json.Unmarshal(projection.Payload, &o); err != nil {
				return err
			}
			if o.Request == uuid.Nil {
				return nil
			}
			n, err := s.store.Request(ctx, tx, o.Request, true)
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if n.Status == "CANCELLED" {
				return nil
			}
			status, remaining, err := s.requestProgress(ctx, tx, n.Request)
			if err != nil {
				return err
			}
			if err := s.store.SetStatus(ctx, tx, o.Request, status, s.clock.Now()); err != nil {
				return err
			}
			if o.Status == "REJECTED" || o.Status == "BROKEN" || (o.Status == "EXPIRED" && o.Next == nil) || (o.Status == "DELIVERED" && o.ConditionsViolated != nil && *o.ConditionsViolated) {
				reason := matching.DeficitSourceRejected
				if o.Status == "BROKEN" || o.Status == "DELIVERED" {
					reason = matching.DeficitUnitWithdrawn
				}
				if o.Status == "EXPIRED" {
					reason = matching.DeficitNotConfirmedInTime
				}
				return s.followUp(ctx, tx, n, o.ID, o.Version, msg.Type, reason, remaining)
			}
		}
		return nil
	})
}

// Старое задание не должно понижать состояние заявки после подтверждения запасного источника.
func (s *Service) requestProgress(ctx context.Context, tx pgx.Tx, request domain.Request) (string, int, error) {
	rows, err := s.store.RequestInputs(ctx, tx, request.RequestID)
	if err != nil {
		return "", 0, err
	}
	delivered, confirmed, transport, dispatched := 0, 0, 0, 0
	pending := false
	known := map[uuid.UUID]bool{}
	knownSources := map[int64]bool{}
	for _, row := range rows {
		if strings.HasPrefix(row.Key, "obligation:") {
			var o obligationProjection
			if err := json.Unmarshal(row.Payload, &o); err != nil {
				return "", 0, err
			}
			known[o.ID] = true
			knownSources[o.Source] = true
		}
	}
	for _, row := range rows {
		if !strings.HasPrefix(row.Key, "obligation:") {
			continue
		}
		var o obligationProjection
		if err := json.Unmarshal(row.Payload, &o); err != nil {
			return "", 0, err
		}
		if o.Request != request.RequestID {
			continue
		}
		switch o.Status {
		case "DELIVERED":
			if o.ConditionsViolated == nil || !*o.ConditionsViolated {
				delivered += len(o.Units)
			}
		case "DISPATCHED":
			dispatched += len(o.Units)
		case "TRANSPORT_REQUESTED":
			transport += len(o.Units)
		case "CONFIRMED":
			confirmed += len(o.Units)
		case "PENDING":
			pending = true
		case "EXPIRED":
			pending = pending || (o.Next != nil && !known[*o.Next])
		}
	}
	remaining := max(0, request.RequestedUnits-delivered-confirmed-transport-dispatched)
	outgoing, err := s.store.OutgoingChoices(ctx, tx, request.RequestID)
	if err != nil {
		return "", 0, err
	}
	for _, c := range outgoing {
		pending = pending || !knownSources[c.Source]
	}
	switch {
	case delivered >= request.RequestedUnits:
		return "DELIVERED", 0, nil
	case dispatched > 0:
		return "DISPATCHED", remaining, nil
	case transport > 0:
		return "TRANSPORT_REQUESTED", remaining, nil
	case confirmed > 0:
		return "CONFIRMED", remaining, nil
	case pending:
		return "TASK_OPENED", remaining, nil
	default:
		return "DEFICIT", remaining, nil
	}
}

func (s *Service) matchInput(ctx context.Context, tx pgx.Tx, request domain.Request) (domain.Input, error) {
	in := domain.Input{Request: request, At: s.clock.Now(), Delivery: map[int64]domain.DeliveryEstimate{}, Stock: map[domain.StockKey]domain.SourceStock{}, Policy: domain.DemoPolicy()}
	in.GroupApplicable = map[string]bool{}
	for _, kind := range s.ref.ComponentKinds {
		in.GroupApplicable[kind.Code] = kind.IsGroupApplicable
	}
	rules, err := s.store.Rules(ctx, tx)
	if err != nil {
		return in, err
	}
	in.Rules = rules
	rows, err := s.store.Inputs(ctx, tx)
	if err != nil {
		return in, err
	}
	var obligations []obligationProjection
	reserved := map[uuid.UUID]bool{}
	for _, p := range rows {
		switch {
		case strings.HasPrefix(p.Key, "unit:"):
			var u unitProjection
			if err := json.Unmarshal(p.Payload, &u); err != nil {
				return in, err
			}
			if u.Expires == nil {
				continue
			}
			in.Units = append(in.Units, domain.Unit{UnitID: u.ID, ClinicID: u.Clinic, ComponentKind: u.Kind, BloodGroup: u.Group, Phenotype: u.Phenotype, VolumeML: u.Volume, ExpiresAt: *u.Expires, Status: u.Status})
		case strings.HasPrefix(p.Key, "protected:"):
			var n inventory.ProtectedVolumePublished
			if err := json.Unmarshal(p.Payload, &n); err != nil {
				return in, err
			}
			if n.VolumeUnits < 0 || n.VolumeUnits > math.MaxInt32 {
				return in, fmt.Errorf("%w: защищённый остаток вне диапазона", ErrInvalid)
			}
			in.Stock[domain.StockKey{ClinicID: n.ClinicID, ComponentKind: n.ComponentKind}] = domain.SourceStock{ProtectedUnits: int(n.VolumeUnits)}
		case strings.HasPrefix(p.Key, "stock:"):
			var n inventory.StockSnapshotPublished
			if err := json.Unmarshal(p.Payload, &n); err != nil {
				return in, err
			}
			in.StockWatermark = max(in.StockWatermark, n.Watermark)
			if n.SyncedAt.After(in.StockSyncedAt) {
				in.StockSyncedAt = n.SyncedAt
			}
		case strings.HasPrefix(p.Key, "delivery:"):
			var n logistics.DeliveryEstimatesPublished
			if err := json.Unmarshal(p.Payload, &n); err != nil {
				return in, err
			}
			for _, route := range n.Estimates {
				if route.TargetClinicID == request.ClinicID {
					in.Delivery[n.SourceClinicID] = domain.DeliveryEstimate{Minutes: route.Minutes, EstimatedAt: n.EstimatedAt}
				}
			}
		case strings.HasPrefix(p.Key, "obligation:"):
			var o obligationProjection
			if err := json.Unmarshal(p.Payload, &o); err != nil {
				return in, err
			}
			obligations = append(obligations, o)
		}
	}
	unitClinics := make(map[uuid.UUID]int64, len(in.Units))
	for _, u := range in.Units {
		unitClinics[u.UnitID] = u.ClinicID
	}
	for _, o := range obligations {
		if o.Status == "CONFIRMED" || o.Status == "TRANSPORT_REQUESTED" || o.Status == "DISPATCHED" {
			for _, id := range o.Units {
				reserved[id] = true
			}
		}
		if o.Status == "DELIVERED" {
			for _, id := range o.Units {
				if clinic, ok := unitClinics[id]; ok && (clinic == o.Source || (o.ConditionsViolated != nil && *o.ConditionsViolated)) {
					reserved[id] = true
				}
			}
		}
	}
	for i := range in.Units {
		u := &in.Units[i]
		if reserved[u.UnitID] {
			u.Status = "RESERVED"
		}
		if u.Status == domain.StatusInStock && u.ExpiresAt.After(in.At) {
			key := domain.StockKey{ClinicID: u.ClinicID, ComponentKind: u.ComponentKind}
			if stock, known := in.Stock[key]; known {
				stock.AvailableUnits++
				in.Stock[key] = stock
			}
		}
	}
	if in.StockWatermark == 0 {
		return in, fmt.Errorf("%w: снимок запаса ещё не получен", ErrInvalid)
	}
	return in, nil
}
