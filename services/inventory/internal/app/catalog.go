package app

import (
	"fmt"

	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/domain"
)

// catalog сопоставляет коды АИСТ-М со справочниками системы: клиника по коду → clinic_id,
// группа крови → резус. Неизвестный код — некорректная запись источника.
type catalog struct {
	clinicByCode map[string]int64
	clinics      map[int64]bool
	kinds        map[string]bool
	rhPositive   map[string]bool
}

func newCatalog(ref *refdata.Set) *catalog {
	c := &catalog{
		clinicByCode: map[string]int64{}, clinics: map[int64]bool{},
		kinds: map[string]bool{}, rhPositive: map[string]bool{},
	}
	for _, cl := range ref.Clinics {
		c.clinicByCode[cl.ClinicCode] = cl.ClinicID
		c.clinics[cl.ClinicID] = true
	}
	for _, k := range ref.ComponentKinds {
		c.kinds[k.Code] = true
	}
	for _, g := range ref.BloodGroups {
		c.rhPositive[g.Code] = g.RhPositive
	}
	return c
}

// source переводит запись АИСТ-М в термины копии.
func (c *catalog) source(u AistUnit) (domain.SourceUnit, error) {
	clinicID, ok := c.clinicByCode[u.ClinicCode]
	if !ok {
		return domain.SourceUnit{}, fmt.Errorf("%w: неизвестная клиника %q", domain.ErrInvalidSource, u.ClinicCode)
	}
	if !c.kinds[u.ComponentKind] {
		return domain.SourceUnit{}, fmt.Errorf("%w: неизвестный вид компонента %q", domain.ErrInvalidSource, u.ComponentKind)
	}
	rh, ok := c.rhPositive[u.BloodGroup]
	if !ok {
		return domain.SourceUnit{}, fmt.Errorf("%w: неизвестная группа крови %q", domain.ErrInvalidSource, u.BloodGroup)
	}
	return domain.SourceUnit{
		AistUnitCode: u.AistUnitCode, ClinicID: clinicID, ComponentKind: u.ComponentKind,
		BloodGroup: u.BloodGroup, RhPositive: rh, Phenotype: u.Phenotype, VolumeML: u.VolumeML,
		ExpiresAt: u.ExpiresAt.UTC(), StorageLocation: u.StorageLocation, Status: u.Status,
		WithdrawalReason: u.WithdrawalReason, Version: u.Version, ChangedAt: u.ChangedAt.UTC(),
	}, nil
}
