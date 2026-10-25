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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/matching"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/k1seal/opener"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/domain"
)

type Service struct {
	pool    *pgxpool.Pool
	store   Store
	journal *messaging.Journal
	ref     *refdata.Set
	clock   clock.Clock
	keys    *opener.Keyring
}

func New(pool *pgxpool.Pool, store Store, journal *messaging.Journal, ref *refdata.Set, clk clock.Clock, keys *opener.Keyring) *Service {
	return &Service{pool: pool, store: store, journal: journal, ref: ref, clock: clk, keys: keys}
}

func allowed(a Actor, n RequestRecord) bool {
	return slices.Contains(a.Roles, "DUTY_OFFICER") || (a.ClinicID == n.Request.ClinicID && n.CreatedBy == a.User && slices.Contains(a.Roles, "PHYSICIAN"))
}

func (s *Service) Create(ctx context.Context, request domain.Request, patient string, actor Actor) (RequestRecord, error) {
	if !slices.Contains(actor.Roles, "PHYSICIAN") || actor.ClinicID <= 0 || request.ClinicID != actor.ClinicID {
		return RequestRecord{}, ErrForbidden
	}
	request.CreatedAt = s.clock.Now()
	if request.RequestID == uuid.Nil {
		var err error
		request.RequestID, err = uuid.NewV7()
		if err != nil {
			return RequestRecord{}, err
		}
	}
	if err := request.Validate(); err != nil {
		return RequestRecord{}, err
	}
	if len(request.Phenotype) > 128 || request.RequestedUnits > 10000 {
		return RequestRecord{}, ErrInvalid
	}
	if _, ok := s.ref.Kind(request.ComponentKind); !ok {
		return RequestRecord{}, ErrInvalid
	}
	sealed, err := s.keys.Seal(patient, request.RequestID.String())
	if err != nil {
		return RequestRecord{}, fmt.Errorf("%w: номер случая не принят", ErrInvalid)
	}
	var out RequestRecord
	err = bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		// Ключ запроса не позволяет двойной отправке формы создать разные заявки.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,1886151025))`, request.RequestID.String()); err != nil {
			return err
		}
		existing, err := s.store.Request(ctx, tx, request.RequestID, false)
		if err == nil {
			if !allowed(actor, existing) {
				return ErrForbidden
			}
			previous := existing.Request
			if previous.ComponentKind != request.ComponentKind || previous.ClinicID != request.ClinicID || previous.RequestedUnits != request.RequestedUnits || previous.BloodGroup != request.BloodGroup || previous.Urgency != request.Urgency || previous.Phenotype != request.Phenotype || !slices.Equal(previous.Constraints, request.Constraints) {
				return ErrConflict
			}
			sealed, err := s.store.Patient(ctx, tx, previous.RequestID)
			if err != nil {
				return err
			}
			plain, err := s.keys.Open(sealed, previous.RequestID.String())
			if err != nil {
				return err
			}
			if plain != strings.TrimSpace(patient) {
				return ErrConflict
			}
			out = existing
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		input, err := s.matchInput(ctx, tx, request)
		if err != nil {
			return err
		}
		result, err := domain.Match(input)
		if err != nil {
			return err
		}
		status := "OFFERS_READY"
		if result.Deficit != nil {
			status = "DEFICIT"
		}
		out = RequestRecord{Request: request, CreatedBy: actor.User, Status: status, Result: result, Version: 1}
		if err := s.store.InsertRequest(ctx, tx, out, sealed); err != nil {
			return err
		}
		if err := s.event(ctx, tx, request.RequestID, matching.ClinicalRequestCreated{ClinicalRequestID: request.RequestID, ClinicID: request.ClinicID, ComponentKind: request.ComponentKind, RequestedUnits: request.RequestedUnits, Urgency: request.Urgency, BloodGroup: request.BloodGroup, CreatedBy: actor.User}); err != nil {
			return err
		}
		if err := s.publishOffers(ctx, tx, out); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, request.RequestID, "CLINICAL_REQUEST_CREATED", auditc.ResultSuccess)
	})
	return out, err
}

func (s *Service) Request(ctx context.Context, id uuid.UUID, actor Actor) (RequestRecord, error) {
	n, err := s.store.Request(ctx, nil, id, false)
	if err != nil {
		return n, err
	}
	if !allowed(actor, n) {
		return RequestRecord{}, ErrForbidden
	}
	rows, err := s.store.RequestInputs(ctx, nil, id)
	if err != nil {
		return RequestRecord{}, err
	}
	n.PatientAllocationStatus, err = patientProgress(rows, n.Status)
	if err != nil {
		return RequestRecord{}, err
	}
	for _, row := range rows {
		if strings.HasPrefix(row.Key, "obligation:") {
			var o obligationProjection
			if err := json.Unmarshal(row.Payload, &o); err != nil {
				return RequestRecord{}, err
			}
			blocked := o.ConditionsViolated != nil && *o.ConditionsViolated
			n.ClinicalUseBlocked = n.ClinicalUseBlocked || blocked
			n.Obligations = append(n.Obligations, RequestObligation{ID: o.ID, Source: o.Source, Status: o.Status, Units: len(o.Units), ClinicalUseBlocked: blocked})
		}
	}
	return n, nil
}
func (s *Service) Requests(ctx context.Context, actor Actor, summary bool) ([]RequestRecord, error) {
	clinic := actor.ClinicID
	if slices.Contains(actor.Roles, "DUTY_OFFICER") {
		clinic = 0
	} else if clinic <= 0 {
		return nil, ErrForbidden
	}
	items, err := s.store.Requests(ctx, clinic, summary)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(items, func(n RequestRecord) bool { return !allowed(actor, n) }), nil
}

func (s *Service) Select(ctx context.Context, id, variantID, choiceID uuid.UUID, actor Actor) (RequestRecord, error) {
	if choiceID == uuid.Nil {
		return RequestRecord{}, ErrInvalid
	}
	err := bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		n, err := s.store.Request(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if !allowed(actor, n) || !slices.Contains(actor.Roles, "PHYSICIAN") {
			return ErrForbidden
		}
		if n.Selection != nil {
			if n.Selection.ChoiceID == choiceID && n.Selection.VariantID == variantID {
				return nil
			}
			return ErrConflict
		}
		if n.Status != "OFFERS_READY" {
			return ErrConflict
		}
		var selected *domain.Variant
		for i := range n.Result.Variants {
			if VariantID(id, n.Result.Variants[i].Rank) == variantID {
				selected = &n.Result.Variants[i]
				break
			}
		}
		if selected == nil {
			return ErrNotFound
		}
		// Повторная оценка выбранных единиц по текущим проекциям без нового клинического выбора.
		input, err := s.matchInput(ctx, tx, n.Request)
		if err != nil {
			return err
		}
		fresh, err := domain.Match(input)
		if err != nil {
			return err
		}
		var current *domain.Variant
		for i := range fresh.Variants {
			v := &fresh.Variants[i]
			if v.SourceClinicID == selected.SourceClinicID && v.ComponentKind == selected.ComponentKind && v.BloodGroup == selected.BloodGroup && v.Rule.RuleID == selected.Rule.RuleID {
				current = v
				break
			}
		}
		if current == nil || len(current.UnitIDs) == 0 {
			return ErrConflict
		}
		status := "LOCAL_SELECTED"
		if !current.IsLocal {
			if !current.ProtectedKnown {
				return fmt.Errorf("%w: защищённый остаток источника ещё не получен", ErrInvalid)
			}
			status = "TASK_OPENED"
		}
		if err := s.store.SaveSelection(ctx, tx, id, Selection{VariantID: variantID, ChoiceID: choiceID, By: actor.User, At: s.clock.Now()}, status, s.clock.Now()); err != nil {
			return err
		}
		if !current.IsLocal {
			if err := s.openRemoteChoice(ctx, tx, n.Request, choiceID, *current, fresh.Variants, nil); err != nil {
				return err
			}
		}
		if err := s.event(ctx, tx, id, matching.DoctorChoiceRecorded{ClinicalRequestID: id, VariantOfferID: variantID, SelectedBy: actor.User, IsLocal: current.IsLocal}); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, id, "VARIANT_SELECTED", auditc.ResultSuccess)
	})
	if err != nil {
		return RequestRecord{}, err
	}
	return s.Request(ctx, id, actor)
}

func (s *Service) Cancel(ctx context.Context, id uuid.UUID, actor Actor) error {
	return bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		n, err := s.store.Request(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if !allowed(actor, n) || !slices.Contains(actor.Roles, "PHYSICIAN") {
			return ErrForbidden
		}
		if n.Status == "CANCELLED" || n.Status == "CANCELLATION_REQUESTED" {
			return nil
		}
		if n.Status == "DISPATCHED" || n.Status == "DELIVERED" || n.Status == "CANCELLATION_REJECTED" {
			return ErrConflict
		}
		if n.Selection != nil && n.Status != "LOCAL_SELECTED" {
			if err := s.store.SetStatus(ctx, tx, id, "CANCELLATION_REQUESTED", s.clock.Now()); err != nil {
				return err
			}
			if _, err := s.journal.Publish(ctx, tx, contracts.TopicTransferCommands, id.String(), transfer.CancelRequestObligations{RequestID: id, Reason: transfer.CancelRequestCancelled}); err != nil {
				return err
			}
			return s.audit(ctx, tx, actor, id, "CLINICAL_REQUEST_CANCEL_REQUESTED", auditc.ResultSuccess)
		}
		if err := s.store.SetStatus(ctx, tx, id, "CANCELLED", s.clock.Now()); err != nil {
			return err
		}
		if _, err := s.journal.Publish(ctx, tx, contracts.TopicTransferCommands, id.String(), transfer.CancelRequestObligations{RequestID: id, Reason: transfer.CancelRequestCancelled}); err != nil {
			return err
		}
		if err := s.event(ctx, tx, id, matching.ClinicalRequestCancelled{ClinicalRequestID: id, CancelledBy: actor.User}); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, id, "CLINICAL_REQUEST_CANCELLED", auditc.ResultSuccess)
	})
}

func (s *Service) event(ctx context.Context, tx pgx.Tx, id uuid.UUID, p contracts.Payload) error {
	_, err := s.journal.Publish(ctx, tx, contracts.TopicMatchingEvents, id.String(), p)
	return err
}
func (s *Service) audit(ctx context.Context, tx pgx.Tx, actor Actor, id uuid.UUID, action, result string, details ...map[string]any) error {
	role := "SERVICE"
	if len(actor.Roles) > 0 {
		role = actor.Roles[0]
	}
	var clinic *int64
	if actor.ClinicID > 0 {
		clinic = &actor.ClinicID
	}
	var metadata map[string]any
	if len(details) > 0 {
		metadata = details[0]
	}
	return s.journal.Audit(ctx, tx, messaging.AuditEntry{UserID: actor.User, Role: role, ClinicID: clinic, Action: action, ObjectType: "clinical_request", ObjectID: id.String(), Result: result, Details: metadata})
}

func (s *Service) publishOffers(ctx context.Context, tx pgx.Tx, n RequestRecord) error {
	offers := make([]matching.VariantOffer, 0, len(n.Result.Variants))
	for _, v := range n.Result.Variants {
		offers = append(offers, matching.VariantOffer{VariantOfferID: VariantID(n.Request.RequestID, v.Rank), RankNo: v.Rank, SourceClinicID: v.SourceClinicID, ComponentKind: v.ComponentKind, BloodGroup: v.BloodGroup, AvailableUnits: v.AvailableUnits, DeliveryMinutes: v.Delivery.Minutes, IsDeficitStockUsed: v.IsDeficitStockUsed, MatchingRuleID: v.Rule.RuleID})
	}
	if err := s.event(ctx, tx, n.Request.RequestID, matching.VariantsOffered{ClinicalRequestID: n.Request.RequestID, Variants: offers, StockWatermark: n.Result.StockWatermark}); err != nil {
		return err
	}
	if n.Result.Deficit != nil {
		return s.event(ctx, tx, n.Request.RequestID, matching.RequestDeficitRecorded{ClinicalRequestID: n.Request.RequestID, ClinicID: n.Request.ClinicID, ComponentKind: n.Request.ComponentKind, BloodGroup: n.Request.BloodGroup, UncoveredUnits: n.Request.RequestedUnits, Reason: matching.DeficitNoAllowedVariant})
	}
	return nil
}
