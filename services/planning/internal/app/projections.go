package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/demand"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/matching"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/domain"
)

// Handle обновляет только собственные копии; inbox и проекция фиксируются одной транзакцией.
func (s *Service) Handle(ctx context.Context, msg messaging.Message) error {
	if msg.Type == "RequestDeficitRecorded" {
		var event matching.RequestDeficitRecorded
		if err := msg.Decode(&event); err != nil {
			return messaging.Permanent(err)
		}
		if event.ClinicalRequestID == uuid.Nil || event.ClinicID <= 0 || event.UncoveredUnits <= 0 {
			return messaging.Permanent(fmt.Errorf("некорректная запись дефицита заявки"))
		}
		return bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
			first, err := messaging.FirstTime(ctx, tx, msg, s.clock.Now())
			if err != nil || !first {
				return err
			}
			return s.store.RecordDeficit(ctx, tx, DeficitRecord{ID: msg.ID, Type: "REQUEST", OriginID: event.ClinicalRequestID, ClinicID: event.ClinicID, Kind: event.ComponentKind, Group: event.BloodGroup, Units: event.UncoveredUnits, Reason: event.Reason, At: msg.OccurredAt})
		})
	}
	key, version, payload, err := projectionMessage(msg)
	if err != nil {
		return messaging.Permanent(err)
	}
	if key == "" {
		return nil
	}
	return bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		first, err := messaging.FirstTime(ctx, tx, msg, s.clock.Now())
		if err != nil || !first {
			return err
		}
		if msg.Type == "TransferUnitReplaced" {
			var replacement struct {
				Old uuid.UUID `json:"old_unit_id"`
				New uuid.UUID `json:"new_unit_id"`
			}
			if err := msg.Decode(&replacement); err != nil {
				return messaging.Permanent(err)
			}
			raw, err := s.store.Projection(ctx, tx, key)
			if errors.Is(err, ErrNotFound) {
				return fmt.Errorf("ожидание исходного обязательства")
			}
			if err != nil {
				return err
			}
			var current obligationInput
			if err := json.Unmarshal(raw, &current); err != nil {
				return err
			}
			for i, id := range current.Units {
				if id == replacement.Old {
					current.Units[i] = replacement.New
				}
			}
			payload, err = json.Marshal(map[string]any{"units": current.Units})
			if err != nil {
				return err
			}
		}
		return s.store.Project(ctx, tx, Projection{Key: key, Version: version, At: msg.OccurredAt, Payload: payload})
	})
}

func projectionMessage(msg messaging.Message) (string, int64, json.RawMessage, error) {
	var key string
	version := int64(-1)
	var payload any
	switch msg.Type {
	case "UnitRegistered", "UnitUpdated", "UnitWithdrawn":
		var u unitInput
		var source struct {
			Version int64  `json:"version"`
			Reason  string `json:"reason"`
		}
		if err := msg.Decode(&u); err != nil {
			return "", 0, nil, err
		}
		if err := msg.Decode(&source); err != nil {
			return "", 0, nil, err
		}
		if u.ID == uuid.Nil || source.Version <= 0 {
			return "", 0, nil, fmt.Errorf("некорректная версия или идентификатор единицы")
		}
		if msg.Type == "UnitWithdrawn" {
			u.Status = source.Reason
		}
		key, version, payload = "unit:"+u.ID.String(), source.Version, u
	case "StockNormPublished":
		var n inventory.StockNormPublished
		if err := msg.Decode(&n); err != nil {
			return "", 0, nil, err
		}
		key, version, payload = fmt.Sprintf("norm:%d:%s", n.ClinicID, n.ComponentKind), n.Version, n
	case "StockSnapshotPublished":
		var n inventory.StockSnapshotPublished
		if err := msg.Decode(&n); err != nil {
			return "", 0, nil, err
		}
		key, version, payload = fmt.Sprintf("stock:%d:%s:%s", n.ClinicID, n.ComponentKind, n.BloodGroup), n.Watermark, n
	case "DemandEstimatePublished":
		var n demand.DemandEstimatePublished
		if err := msg.Decode(&n); err != nil {
			return "", 0, nil, err
		}
		key, version, payload = "demand:latest", int64(n.VersionNo), n
	case "ClinicSharesPublished":
		var n demand.ClinicSharesPublished
		if err := msg.Decode(&n); err != nil {
			return "", 0, nil, err
		}
		key, version, payload = "shares:"+n.ClinicShareVersionID.String(), int64(n.VersionNo), n
	case "DeliveryEstimatesPublished":
		var n logistics.DeliveryEstimatesPublished
		if err := msg.Decode(&n); err != nil {
			return "", 0, nil, err
		}
		key, version, payload = fmt.Sprintf("delivery:%d", n.SourceClinicID), n.EstimatedAt.UnixNano(), n
	case "ObligationOpened", "TransferConfirmed", "TransportRequested", "TransferRejected", "TransferExpired", "TransferCancelled", "TransferBroken", "TransferUnitReplaced", "TransferDispatched", "TransferDelivered":
		var n obligationInput
		var meta struct {
			Version *int64 `json:"obligation_version"`
		}
		if err := msg.Decode(&n); err != nil {
			return "", 0, nil, err
		}
		if err := msg.Decode(&meta); err != nil {
			return "", 0, nil, err
		}
		if n.ID == uuid.Nil {
			return "", 0, nil, fmt.Errorf("нет идентификатора обязательства")
		}
		if meta.Version != nil {
			version = *meta.Version
		}
		n.Status = map[string]string{"ObligationOpened": "PENDING", "TransferConfirmed": "CONFIRMED", "TransportRequested": "TRANSPORT_REQUESTED", "TransferRejected": "REJECTED", "TransferExpired": "EXPIRED", "TransferCancelled": "CANCELLED", "TransferBroken": "BROKEN", "TransferDispatched": "DISPATCHED", "TransferDelivered": "DELIVERED"}[msg.Type]
		if msg.Type == "TransferUnitReplaced" {
			key = "obligation:" + n.ID.String()
			raw, _ := json.Marshal(map[string]any{})
			return key, version, raw, nil
		}
		key, payload = "obligation:"+n.ID.String(), n
	default:
		return "", 0, nil, nil
	}
	raw, err := json.Marshal(payload)
	return key, version, raw, err
}

type inputSnapshot struct {
	domain.Input
	Demand demand.DemandEstimatePublished
}

func (s *Service) inputSnapshot(ctx context.Context, tx pgx.Tx, id uuid.UUID) (inputSnapshot, error) {
	in := inputSnapshot{Input: domain.Input{PlanID: id, Params: domain.DefaultParams()}}
	in.Versions.RefdataVersion = s.ref.Version
	for _, kind := range s.ref.ComponentKinds {
		if kind.IsActive {
			in.Kinds = append(in.Kinds, domain.Kind{Code: kind.Code, TypicalVolumeML: kind.TypicalVolumeML, GroupApplicable: kind.IsGroupApplicable})
		}
	}
	for _, group := range s.ref.BloodGroups {
		in.GroupShares = append(in.GroupShares, domain.GroupShare{Group: group.Code, Share: group.PopulationShare})
	}
	rows, err := s.store.Inputs(ctx, tx)
	if err != nil {
		return in, err
	}
	units := map[uuid.UUID]domain.Unit{}
	var obligations []obligationInput
	shares := map[uuid.UUID]demand.ClinicSharesPublished{}
	for _, row := range rows {
		switch {
		case strings.HasPrefix(row.Key, "unit:"):
			var u unitInput
			if err := json.Unmarshal(row.Payload, &u); err != nil {
				return in, err
			}
			if u.Expires == nil || u.Volume <= 0 || u.Clinic <= 0 {
				continue
			}
			n := domain.Unit{UnitID: u.ID, ClinicID: u.Clinic, Kind: u.Kind, Group: u.Group, VolumeML: u.Volume, ExpiresAt: *u.Expires, Status: u.Status}
			in.Units = append(in.Units, n)
			units[u.ID] = n
		case strings.HasPrefix(row.Key, "norm:"):
			var n inventory.StockNormPublished
			if err := json.Unmarshal(row.Payload, &n); err != nil {
				return in, err
			}
			in.Norms = append(in.Norms, domain.Norm{ClinicID: n.ClinicID, Kind: n.ComponentKind, NormML: n.NormML})
			in.Versions.NormVersion = max(in.Versions.NormVersion, n.Version)
		case row.Key == "demand:latest":
			var n demand.DemandEstimatePublished
			if err := json.Unmarshal(row.Payload, &n); err != nil {
				return in, err
			}
			in.Versions.DemandEstimateID = n.DemandEstimateID
			in.Demand = n
			in.Versions.DemandVersionNo = n.VersionNo
			for _, line := range n.Lines {
				in.Background = append(in.Background, domain.Background{ClinicID: line.ClinicID, Kind: line.ComponentKind, Group: line.BloodGroup, VolumeML: line.DemandVolumeML})
			}
		case strings.HasPrefix(row.Key, "shares:"):
			var n demand.ClinicSharesPublished
			if err := json.Unmarshal(row.Payload, &n); err != nil {
				return in, err
			}
			shares[n.ClinicShareVersionID] = n
		case strings.HasPrefix(row.Key, "stock:"):
			var n inventory.StockSnapshotPublished
			if err := json.Unmarshal(row.Payload, &n); err != nil {
				return in, err
			}
			in.Versions.StockWatermark = max(in.Versions.StockWatermark, n.Watermark)
			if n.SyncedAt.After(in.Versions.StockSyncedAt) {
				in.Versions.StockSyncedAt = n.SyncedAt
			}
		case strings.HasPrefix(row.Key, "delivery:"):
			var n logistics.DeliveryEstimatesPublished
			if err := json.Unmarshal(row.Payload, &n); err != nil {
				return in, err
			}
			for _, line := range n.Estimates {
				in.Delivery = append(in.Delivery, domain.DeliveryEstimate{SourceClinicID: n.SourceClinicID, TargetClinicID: line.TargetClinicID, Minutes: line.Minutes, EstimatedAt: n.EstimatedAt})
			}
			in.Versions.DeliveryEstimatesVersion = max(in.Versions.DeliveryEstimatesVersion, row.Version)
		case strings.HasPrefix(row.Key, "obligation:"):
			var n obligationInput
			if err := json.Unmarshal(row.Payload, &n); err != nil {
				return in, err
			}
			obligations = append(obligations, n)
			in.Versions.ObligationsWatermark = max(in.Versions.ObligationsWatermark, row.Version)
		}
	}
	for _, o := range obligations {
		n := domain.Obligation{ObligationID: o.ID, SourceClinicID: o.Source, TargetClinicID: o.Target, Kind: o.Kind, Units: len(o.Units), UnitIDs: o.Units, Status: o.Status}
		for _, id := range o.Units {
			if u, ok := units[id]; ok {
				n.VolumeML += u.VolumeML
				n.Group = u.Group
			}
		}
		if n.VolumeML == 0 && len(o.Units) > 0 {
			if kind, ok := s.ref.Kind(o.Kind); ok {
				n.VolumeML = int64(len(o.Units)) * kind.TypicalVolumeML
			}
		}
		in.Obligations = append(in.Obligations, n)
	}
	if in.Versions.DemandEstimateID == uuid.Nil {
		return in, fmt.Errorf("%w: опубликованная потребность ещё не получена", ErrInvalid)
	}
	if in.Versions.StockWatermark == 0 {
		return in, fmt.Errorf("%w: снимок запаса ещё не получен", ErrInvalid)
	}
	if selected, ok := shares[in.Demand.ClinicShareVersionID]; ok {
		for _, line := range selected.Shares {
			in.ClinicShares = append(in.ClinicShares, domain.ClinicShare{DistrictID: line.DistrictID, ClinicID: line.ClinicID, Share: line.Share})
		}
	}
	knownNorm := map[string]bool{}
	for _, norm := range in.Norms {
		knownNorm[fmt.Sprintf("%d:%s", norm.ClinicID, norm.Kind)] = true
	}
	for _, unit := range in.Units {
		if unit.Status == domain.UnitStatusInStock && unit.ExpiresAt.After(s.clock.Now()) && !knownNorm[fmt.Sprintf("%d:%s", unit.ClinicID, unit.Kind)] {
			return in, fmt.Errorf("%w: для источника %d и вида %s ещё не получен норматив", ErrInvalid, unit.ClinicID, unit.Kind)
		}
	}
	return in, nil
}
