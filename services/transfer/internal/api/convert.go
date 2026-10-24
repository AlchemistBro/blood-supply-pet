package api

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/api/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// toObligation — задание для ответа. Фенотип спецификации не отдаётся (квазиидентификатор, ADR-0011):
// оператор видит только, что требование к фенотипу есть.
func toObligation(v app.ObligationView) gen.Obligation {
	units := v.Units
	if units == nil {
		units = []uuid.UUID{}
	}
	fallback := v.FallbackSources
	if fallback == nil {
		fallback = []int64{}
	}
	return gen.Obligation{
		PatientAllocationStatus: v.PatientAllocationStatus, PatientAllocationAcknowledgedAt: v.PatientAllocationAt,
		ObligationId: v.ID, ChoiceId: v.ChoiceID, RequestId: v.RequestID, Origin: gen.ObligationOrigin(v.Origin),
		SourceClinicId: v.SourceClinicID, TargetClinicId: v.TargetClinicID, ComponentKind: v.ComponentKind,
		BloodGroup: v.BloodGroup, RequestedQty: v.RequestedQty, ConfirmedQty: v.ConfirmedQty,
		Status: gen.ObligationStatus(v.Status), StatusReason: nonEmpty(&v.StatusReason), Urgency: v.UrgencyCode,
		Deadline: v.Deadline, FallbackSources: fallback, TransportRef: nonEmpty(&v.TransportRef), Units: units,
		Spec: gen.SpecView{
			BloodGroup: v.Spec.BloodGroup, RhPositive: v.Spec.RhPositive, MinExpiresAt: v.Spec.MinExpiresAt,
			RuleVersion: v.Spec.RuleVersion, PhenotypeRequired: v.Spec.Phenotype != nil && *v.Spec.Phenotype != "",
		},
		Version: v.Version, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}
}

// confirmationResponse — исход подтверждения в HTTP: 200 — задание обработано (CONFIRMED, REJECTED),
// 409 — конфликт с состоянием (снято, недоступны, заняты), 422 — запрос не подходит (NFR013, выбор),
// 503 — АИСТ-М не ответила, повторите.
func confirmationResponse(res domain.ConfirmationResult) (gen.ConfirmTransferResponseObject, error) {
	body := gen.ConfirmationResult{Code: gen.ConfirmationResultCode(res.Code()), Message: res.Message()}
	switch r := res.(type) {
	case domain.Confirmed:
		body.ObligationId, body.Units, body.Shortfall = &r.ObligationID, units(r.Units), &r.Shortfall
		return gen.ConfirmTransfer200JSONResponse{ConfirmationJSONResponse: gen.ConfirmationJSONResponse(body)}, nil
	case domain.Rejected:
		reason := string(r.Reason)
		body.ObligationId, body.Reason = &r.ObligationID, &reason
		return gen.ConfirmTransfer200JSONResponse{ConfirmationJSONResponse: gen.ConfirmationJSONResponse(body)}, nil
	case domain.TaskWithdrawn:
		st := gen.ObligationStatus(r.Status)
		body.ObligationId, body.Status = &r.ObligationID, &st
		return gen.ConfirmTransfer409JSONResponse(body), nil
	case domain.UnitsUnavailable:
		body.Units = units(r.Units)
		return gen.ConfirmTransfer409JSONResponse(body), nil
	case domain.UnitsTaken:
		body.Units = units(r.Units)
		return gen.ConfirmTransfer409JSONResponse(body), nil
	case domain.ProtectedVolumeExceeded:
		body.MaxConfirmable = &r.MaxConfirmable
		return gen.ConfirmTransfer422JSONResponse(body), nil
	case domain.InvalidRequest:
		body.Reason = &r.Reason
		return gen.ConfirmTransfer422JSONResponse(body), nil
	case domain.SourceSystemUnavailable:
		return gen.ConfirmTransfer503JSONResponse(body), nil
	default:
		return nil, fmt.Errorf("неизвестный исход подтверждения %T", res)
	}
}

// replacementResponse — исход замены в HTTP по тем же правилам, что и подтверждение.
func replacementResponse(res domain.ReplacementResult) (gen.ReplaceUnitResponseObject, error) {
	body := gen.ReplacementResult{Code: gen.ReplacementResultCode(res.Code()), Message: res.Message()}
	switch r := res.(type) {
	case domain.Replaced:
		body.ObligationId, body.OldUnitId, body.NewUnitId = &r.ObligationID, &r.OldUnitID, &r.NewUnitID
		return gen.ReplaceUnit200JSONResponse{ReplacementJSONResponse: gen.ReplacementJSONResponse(body)}, nil
	case domain.NotAllowed:
		st := gen.ObligationStatus(r.Status)
		body.Status = &st
		return gen.ReplaceUnit409JSONResponse(body), nil
	case domain.UnitUnavailable:
		body.UnitId = &r.UnitID
		return gen.ReplaceUnit409JSONResponse(body), nil
	case domain.UnitTaken:
		body.UnitId = &r.UnitID
		return gen.ReplaceUnit409JSONResponse(body), nil
	case domain.SpecMismatch:
		reason := string(r.Reason)
		body.Reason = &reason
		return gen.ReplaceUnit422JSONResponse(body), nil
	case domain.SourceSystemUnavailable:
		return gen.ReplaceUnit503JSONResponse(body), nil
	default:
		return nil, fmt.Errorf("неизвестный исход замены %T", res)
	}
}

func units(ids []uuid.UUID) *[]uuid.UUID {
	if ids == nil {
		ids = []uuid.UUID{}
	}
	return &ids
}

func nonEmpty(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	v := *s
	return &v
}
