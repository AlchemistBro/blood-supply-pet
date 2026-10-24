package app

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

// ActionPublishProtectedVolume — действие аудита демо-публикации защищённого объёма.
const ActionPublishProtectedVolume = "PUBLISH_PROTECTED_VOLUME"

// DemoProtectedVolumeReason — основание по умолчанию: объём задан вручную (ADR-0010, демо).
const DemoProtectedVolumeReason = "ДЕМО: задан вручную до появления planning"

// Actor — кто выполняет операцию (из токена).
type Actor struct {
	UserID   string
	Role     string
	ClinicID *int64
}

// PublishProtectedVolume публикует ProtectedVolumePublished (демо, только BS_ENV=local): до появления planning
// защищённый объём источника задаётся вручную. Версия растёт только вперёд; событие и аудит — одна транзакция.
func (s *Service) PublishProtectedVolume(ctx context.Context, actor Actor, pv domain.ProtectedVolume) (domain.ProtectedVolume, error) {
	if err := pv.Validate(); err != nil {
		return domain.ProtectedVolume{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if _, ok := s.ref.Clinic(pv.ClinicID); !ok {
		return domain.ProtectedVolume{}, fmt.Errorf("%w: неизвестная клиника %d", ErrInvalid, pv.ClinicID)
	}
	if _, ok := s.ref.Kind(pv.ComponentKind); !ok {
		return domain.ProtectedVolume{}, fmt.Errorf("%w: неизвестный вид компонента %q", ErrInvalid, pv.ComponentKind)
	}
	if pv.Reason == "" {
		pv.Reason = DemoProtectedVolumeReason
	}
	err := bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		v, err := s.store.NextProtectedVolumeVersion(ctx, tx)
		if err != nil {
			return err
		}
		pv.Version = v
		if err := s.pub.PublishProtectedVolume(ctx, tx, pv); err != nil {
			return err
		}
		return s.journal.Audit(ctx, tx, messaging.AuditEntry{
			UserID: actor.UserID, Role: actor.Role, ClinicID: actor.ClinicID,
			Action: ActionPublishProtectedVolume, ObjectType: "protected_volume", ObjectID: pv.Key(),
			Result:  auditc.ResultSuccess,
			Details: map[string]any{"volume_units": pv.VolumeUnits, "version": pv.Version, "reason": pv.Reason},
		})
	})
	if err != nil {
		return domain.ProtectedVolume{}, err
	}
	return pv, nil
}
