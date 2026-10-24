package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
)

// PatientCase возвращает открытый номер только после фиксации аудита.
// Отказ также фиксируется; ошибка аудита никогда не превращается в выдачу номера.
func (s *Service) PatientCase(ctx context.Context, id uuid.UUID, actor Actor) (string, error) {
	var plain string
	denied := false
	err := bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		n, err := s.store.Request(ctx, tx, id, false)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		permit := err == nil && actor.User == n.CreatedBy && actor.ClinicID == n.Request.ClinicID && slices.Contains(actor.Roles, "PHYSICIAN")
		if err == nil && slices.Contains(actor.Roles, "CLINIC_OPERATOR") && actor.ClinicID > 0 {
			rows, err := s.store.RequestInputs(ctx, tx, id)
			if err != nil {
				return err
			}
			for _, p := range rows {
				if !strings.HasPrefix(p.Key, "obligation:") {
					continue
				}
				var o obligationProjection
				if err := json.Unmarshal(p.Payload, &o); err != nil {
					return err
				}
				if o.Request == id && o.Source == actor.ClinicID && (o.Status == "CONFIRMED" || o.Status == "TRANSPORT_REQUESTED" || o.Status == "DISPATCHED") {
					permit = true
					break
				}
			}
		}
		result := auditc.ResultDenied
		if permit {
			result = auditc.ResultSuccess
		}
		if err := s.audit(ctx, tx, actor, id, "READ_PATIENT_CASE", result); err != nil {
			return err
		}
		if !permit {
			denied = true
			return nil
		}
		sealed, err := s.store.Patient(ctx, tx, id)
		if err != nil {
			return err
		}
		plain, err = s.keys.Open(sealed, id.String())
		return err
	})
	if err != nil {
		return "", err
	}
	if denied {
		return "", ErrForbidden
	}
	return plain, nil
}

func (s *Service) RecordReadDenied(ctx context.Context, id uuid.UUID, actor Actor) error {
	return bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		return s.audit(ctx, tx, actor, id, "READ_PATIENT_CASE", auditc.ResultDenied)
	})
}
