package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/matching"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/domain"
)

func (s *Service) openRemoteChoice(ctx context.Context, tx pgx.Tx, request domain.Request, choiceID uuid.UUID, current domain.Variant, variants []domain.Variant, attempted map[int64]bool) error {
	sealed, err := s.store.Patient(ctx, tx, request.RequestID)
	if err != nil {
		return err
	}
	group, expires := current.Spec.BloodGroup, current.Spec.MinExpiresAt
	window := int64(86400)
	if request.Urgency == transfer.UrgencyUrgent {
		window = 3600
	}
	if request.Urgency == transfer.UrgencyEmergency {
		window = 900
	}
	command := transfer.OpenObligation{ChoiceID: choiceID, RequestID: request.RequestID, Origin: transfer.OriginDoctorChoice, SourceClinicID: current.SourceClinicID, TargetClinicID: request.ClinicID, ComponentKind: current.ComponentKind, RequestedQty: request.RequestedUnits, Urgency: request.Urgency, ConfirmWindowSeconds: window, PatientCaseSealed: string(sealed), ProtectedFloors: map[int64]int{current.SourceClinicID: current.Protected.ProtectedUnits}, Spec: transfer.ReplacementSpec{BloodGroup: &group, Phenotype: current.Spec.Phenotype, MinExpiresAt: &expires, RuleVersion: fmt.Sprintf("%s/v%d", current.Spec.RuleCode, current.Spec.RuleVersion)}}
	if err := s.store.InsertOutgoingChoice(ctx, tx, request.RequestID, OutgoingChoice{ChoiceID: choiceID, Source: current.SourceClinicID, Quantity: request.RequestedUnits}); err != nil {
		return err
	}
	for _, v := range variants {
		if v.Rank <= current.Rank || v.IsLocal || v.SourceClinicID == current.SourceClinicID || attempted[v.SourceClinicID] || !v.ProtectedKnown || v.ComponentKind != current.ComponentKind || v.BloodGroup != current.BloodGroup || v.Rule.RuleID != current.Rule.RuleID || slices.Contains(command.FallbackSources, v.SourceClinicID) {
			continue
		}
		command.FallbackSources = append(command.FallbackSources, v.SourceClinicID)
		command.ProtectedFloors[v.SourceClinicID] = v.Protected.ProtectedUnits
		if v.Spec.MinExpiresAt.After(*command.Spec.MinExpiresAt) {
			*command.Spec.MinExpiresAt = v.Spec.MinExpiresAt
		}
	}
	_, err = s.journal.Publish(ctx, tx, contracts.TopicTransferCommands, choiceID.String(), command)
	return err
}

// followUp не меняет клинический выбор: тот же вид, группа и версия правила; местный вариант автоматически не выбирается.
func (s *Service) followUp(ctx context.Context, tx pgx.Tx, n RequestRecord, parent uuid.UUID, version int64, eventType, reason string, remaining int) error {
	if n.Selection == nil || remaining <= 0 || cancellationRequested(n.Status) {
		return nil
	}
	rows, err := s.store.RequestInputs(ctx, tx, n.Request.RequestID)
	if err != nil {
		return err
	}
	attempted := map[int64]bool{}
	knownIDs := map[uuid.UUID]bool{}
	for _, row := range rows {
		if strings.HasPrefix(row.Key, "obligation:") {
			var o obligationProjection
			if err := json.Unmarshal(row.Payload, &o); err != nil {
				return err
			}
			knownIDs[o.ID] = true
		}
	}
	for _, row := range rows {
		if !strings.HasPrefix(row.Key, "obligation:") {
			continue
		}
		var o obligationProjection
		if err := json.Unmarshal(row.Payload, &o); err != nil {
			return err
		}
		attempted[o.Source] = true
		if o.Status == "PENDING" {
			remaining -= o.RequestedQty
		}
		if o.Status == "EXPIRED" && o.Next != nil && !knownIDs[*o.Next] {
			remaining -= o.RequestedQty
		}
	}
	outgoing, err := s.store.OutgoingChoices(ctx, tx, n.Request.RequestID)
	if err != nil {
		return err
	}
	for _, c := range outgoing {
		if !attempted[c.Source] {
			remaining -= c.Quantity
		}
		attempted[c.Source] = true
	}
	if remaining <= 0 {
		return nil
	}
	first, err := s.store.ClaimFollowUp(ctx, tx, parent, version, eventType)
	if err != nil || !first {
		return err
	}
	var original *domain.Variant
	for i := range n.Result.Variants {
		v := &n.Result.Variants[i]
		if VariantID(n.Request.RequestID, v.Rank) == n.Selection.VariantID {
			original = v
			break
		}
	}
	if original == nil {
		return ErrConflict
	}
	request := n.Request
	request.RequestedUnits = remaining
	input, err := s.matchInput(ctx, tx, request)
	if err != nil {
		return err
	}
	fresh, err := domain.Match(input)
	if err != nil {
		return err
	}
	for _, v := range fresh.Variants {
		if v.IsLocal || attempted[v.SourceClinicID] || !v.ProtectedKnown || v.ComponentKind != original.ComponentKind || v.BloodGroup != original.BloodGroup || v.Rule.RuleID != original.Rule.RuleID {
			continue
		}
		choice := uuid.NewSHA1(parent, []byte(fmt.Sprintf("followup:%s:%d", eventType, version)))
		if err := s.openRemoteChoice(ctx, tx, request, choice, v, fresh.Variants, attempted); err != nil {
			return err
		}
		if err := s.store.SetStatus(ctx, tx, request.RequestID, "TASK_OPENED", s.clock.Now()); err != nil {
			return err
		}
		return s.audit(ctx, tx, Actor{User: "matching", ClinicID: request.ClinicID}, request.RequestID, "MATCHING_FALLBACK_PROPOSED", auditc.ResultSuccess)
	}
	return s.event(ctx, tx, request.RequestID, matching.RequestDeficitRecorded{ClinicalRequestID: request.RequestID, ClinicID: request.ClinicID, ComponentKind: request.ComponentKind, BloodGroup: request.BloodGroup, UncoveredUnits: remaining, Reason: reason})
}

func (s *Service) shortfall(ctx context.Context, msg messaging.Message) error {
	var event transfer.TransferShortfallRecorded
	if err := msg.Decode(&event); err != nil {
		return messaging.Permanent(err)
	}
	if event.RequestID == uuid.Nil || event.ObligationID == uuid.Nil || event.Shortfall <= 0 {
		return messaging.Permanent(fmt.Errorf("неверная недопоставка"))
	}
	return bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		first, err := messaging.FirstTime(ctx, tx, msg, s.clock.Now())
		if err != nil || !first {
			return err
		}
		n, err := s.store.Request(ctx, tx, event.RequestID, true)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if cancellationRequested(n.Status) {
			return nil
		}
		_, remaining, err := s.requestProgress(ctx, tx, n.Request)
		if err != nil {
			return err
		}
		version := int64(0)
		if event.ObligationVersion != nil {
			version = *event.ObligationVersion
		}
		return s.followUp(ctx, tx, n, event.ObligationID, version, msg.Type, matching.DeficitNoAllowedVariant, min(remaining, event.Shortfall))
	})
}
