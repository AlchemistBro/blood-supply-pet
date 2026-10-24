package app

import (
	"context"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/planning"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

// HandlePlanning ретранслирует расчёт в пространство версий владельца запаса.
// Версия planning и версия inventory независимы; поздний старый расчёт отбрасывается.
func (s *Service) HandlePlanning(ctx context.Context, msg messaging.Message) error {
	if msg.Type != "ProtectedVolumeCalculated" {
		return nil
	}
	var event planning.ProtectedVolumeCalculated
	if err := msg.Decode(&event); err != nil {
		return messaging.Permanent(err)
	}
	if _, ok := s.ref.Clinic(event.ClinicID); !ok {
		return messaging.Permanent(fmt.Errorf("неизвестная клиника расчёта"))
	}
	if _, ok := s.ref.Kind(event.ComponentKind); !ok {
		return messaging.Permanent(fmt.Errorf("неизвестный вид компонента расчёта"))
	}
	if event.Version <= 0 || event.VolumeUnits < 0 || event.VolumeUnits > math.MaxInt32 {
		return messaging.Permanent(fmt.Errorf("некорректная версия или порог расчёта"))
	}
	return bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		first, err := messaging.FirstTime(ctx, tx, msg, s.clock.Now())
		if err != nil || !first {
			return err
		}
		// Единый порядок публикации и выделения версий, в том числе для ручного демо-ввода.
		version, err := s.store.NextProtectedVolumeVersion(ctx, tx)
		if err != nil {
			return err
		}
		changed, err := tx.Exec(ctx, `INSERT INTO planning_floor_version(clinic_id,component_kind,version) VALUES($1,$2,$3)
		 ON CONFLICT(clinic_id,component_kind) DO UPDATE SET version=excluded.version
		 WHERE planning_floor_version.version < excluded.version`, event.ClinicID, event.ComponentKind, event.Version)
		if err != nil {
			return err
		}
		if changed.RowsAffected() == 0 {
			return nil
		}
		return s.pub.PublishProtectedVolume(ctx, tx, domain.ProtectedVolume{ClinicID: event.ClinicID, ComponentKind: event.ComponentKind, VolumeUnits: event.VolumeUnits, Version: version, Reason: event.Reason})
	})
}
