// Package events — публикация событий inventory.events через outbox (libs/messaging) по контракту
// libs/contracts/inventory. Исходный код единицы АИСТ-М в события не попадает (ADR-0010).
package events

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

// Publisher реализует app.Publisher.
type Publisher struct {
	journal *messaging.Journal
	topic   string
}

var _ app.Publisher = (*Publisher)(nil)

// New создаёт публикатора в топик inventory.events.
func New(journal *messaging.Journal) *Publisher {
	return &Publisher{journal: journal, topic: contracts.TopicInventoryEvents}
}

// Payload строит тело события по изменению единицы; ok = false — публиковать нечего.
func Payload(ch domain.Change) (contracts.Payload, bool) {
	u := ch.Unit
	switch ch.Event {
	case domain.EventRegistered:
		return inventory.UnitRegistered{
			UnitID: u.UnitID, ClinicID: u.ClinicID, ComponentKind: u.ComponentKind, BloodGroup: u.BloodGroup,
			RhPositive: u.RhPositive, Phenotype: u.Phenotype, VolumeML: u.VolumeML, ExpiresAt: u.ExpiresAt,
			StorageLocation: u.StorageLocation, Status: u.Status, Version: u.Version,
		}, true
	case domain.EventUpdated:
		expires := u.ExpiresAt
		return inventory.UnitUpdated{
			UnitID: u.UnitID, ClinicID: u.ClinicID, ComponentKind: u.ComponentKind, Status: u.Status,
			ExpiresAt: &expires, StorageLocation: u.StorageLocation, Version: u.Version, ChangedAt: u.ChangedAt,
		}, true
	case domain.EventWithdrawn:
		return inventory.UnitWithdrawn{
			UnitID: u.UnitID, ClinicID: u.ClinicID, ComponentKind: u.ComponentKind, Reason: ch.Reason, Version: u.Version,
		}, true
	}
	return nil, false
}

// PublishUnit пишет событие об единице в outbox; ключ — unit_id (порядок событий единицы в партиции).
func (p *Publisher) PublishUnit(ctx context.Context, tx pgx.Tx, ch domain.Change) error {
	payload, ok := Payload(ch)
	if !ok {
		return nil
	}
	if _, err := p.journal.Publish(ctx, tx, p.topic, ch.Unit.UnitID.String(), payload); err != nil {
		return fmt.Errorf("событие %s: %w", payload.MessageType(), err)
	}
	return nil
}

// PublishProtectedVolume пишет защищённый объём в outbox; ключ — clinic_id:kind.
func (p *Publisher) PublishProtectedVolume(ctx context.Context, tx pgx.Tx, pv domain.ProtectedVolume) error {
	payload := inventory.ProtectedVolumePublished{
		ClinicID: pv.ClinicID, ComponentKind: pv.ComponentKind, VolumeUnits: pv.VolumeUnits,
		Version: pv.Version, Reason: pv.Reason,
	}
	if _, err := p.journal.Publish(ctx, tx, p.topic, pv.Key(), payload); err != nil {
		return fmt.Errorf("событие %s: %w", payload.MessageType(), err)
	}
	return nil
}
