package app

import "context"

// SeedDemoNorms использует обычный сценарий записи с outbox и аудитом; существующие значения сохраняются.
func (s *Service) SeedDemoNorms(ctx context.Context, actor Actor, year int) (int, error) {
	if year < 2000 || year > 2100 {
		return 0, ErrInvalid
	}
	created := 0
	for _, clinic := range s.ref.Clinics {
		current, err := s.Norms(ctx, clinic.ClinicID)
		if err != nil {
			return created, err
		}
		present := map[string]bool{}
		for _, n := range current {
			present[n.Kind] = true
		}
		for _, kind := range s.ref.ComponentKinds {
			if !kind.IsActive || present[kind.Code] {
				continue
			}
			_, added, err := s.setAnnualConsumption(ctx, actor, clinic.ClinicID, kind.Code, year, 36000, true)
			if err != nil {
				return created, err
			}
			if added {
				created++
			}
		}
	}
	return created, nil
}
