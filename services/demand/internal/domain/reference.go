package domain

import (
	"errors"
	"fmt"
)

// DistrictRef — район из справочника ref_district.
type DistrictRef struct {
	ID   int64
	Code string
}

// ClinicRef — клиника из справочника ref_clinic.
type ClinicRef struct {
	ID   int64
	Code string
}

// KindRef — вид компонента: признак применимости группы AB0/Rh решает, есть ли разрез по группам (FR019).
type KindRef struct {
	Code              string
	IsGroupApplicable bool
}

// Reference — справочники, нужные проверке файлов и расчёту. Порядок видов и групп задаёт порядок строк
// результата и разбор равных остатков при округлении.
type Reference struct {
	districtByCode map[string]DistrictRef
	districtByID   map[int64]DistrictRef
	clinicByCode   map[string]ClinicRef
	clinicByID     map[int64]ClinicRef
	kinds          []KindRef
	kindIndex      map[string]int
	groups         []string
	groupIndex     map[string]int
}

// NewReference собирает справочник и проверяет уникальность кодов и идентификаторов.
func NewReference(districts []DistrictRef, clinics []ClinicRef, kinds []KindRef, bloodGroups []string) (*Reference, error) {
	if len(districts) == 0 || len(clinics) == 0 || len(kinds) == 0 || len(bloodGroups) == 0 {
		return nil, errors.New("справочник: районы, клиники, виды и группы не могут быть пустыми")
	}
	r := &Reference{
		districtByCode: make(map[string]DistrictRef, len(districts)),
		districtByID:   make(map[int64]DistrictRef, len(districts)),
		clinicByCode:   make(map[string]ClinicRef, len(clinics)),
		clinicByID:     make(map[int64]ClinicRef, len(clinics)),
		kinds:          append([]KindRef(nil), kinds...),
		kindIndex:      make(map[string]int, len(kinds)),
		groups:         append([]string(nil), bloodGroups...),
		groupIndex:     make(map[string]int, len(bloodGroups)),
	}
	for _, d := range districts {
		if _, dup := r.districtByCode[d.Code]; dup || d.Code == "" {
			return nil, fmt.Errorf("справочник: код района %q пуст или повторяется", d.Code)
		}
		if _, dup := r.districtByID[d.ID]; dup {
			return nil, fmt.Errorf("справочник: идентификатор района %d повторяется", d.ID)
		}
		r.districtByCode[d.Code] = d
		r.districtByID[d.ID] = d
	}
	for _, c := range clinics {
		if _, dup := r.clinicByCode[c.Code]; dup || c.Code == "" {
			return nil, fmt.Errorf("справочник: код клиники %q пуст или повторяется", c.Code)
		}
		if _, dup := r.clinicByID[c.ID]; dup {
			return nil, fmt.Errorf("справочник: идентификатор клиники %d повторяется", c.ID)
		}
		r.clinicByCode[c.Code] = c
		r.clinicByID[c.ID] = c
	}
	for i, k := range kinds {
		if _, dup := r.kindIndex[k.Code]; dup || k.Code == "" {
			return nil, fmt.Errorf("справочник: код вида %q пуст или повторяется", k.Code)
		}
		r.kindIndex[k.Code] = i
	}
	for i, g := range bloodGroups {
		if _, dup := r.groupIndex[g]; dup || g == "" {
			return nil, fmt.Errorf("справочник: группа %q пуста или повторяется", g)
		}
		r.groupIndex[g] = i
	}
	return r, nil
}

// District находит район по коду.
func (r *Reference) District(code string) (DistrictRef, bool) {
	d, ok := r.districtByCode[code]
	return d, ok
}

// Clinic находит клинику по коду.
func (r *Reference) Clinic(code string) (ClinicRef, bool) {
	c, ok := r.clinicByCode[code]
	return c, ok
}

// Kind находит вид компонента по коду.
func (r *Reference) Kind(code string) (KindRef, bool) {
	i, ok := r.kindIndex[code]
	if !ok {
		return KindRef{}, false
	}
	return r.kinds[i], true
}

// BloodGroups — коды групп AB0/Rh в порядке справочника.
func (r *Reference) BloodGroups() []string {
	return append([]string(nil), r.groups...)
}

func (r *Reference) districtCode(id int64) string {
	if d, ok := r.districtByID[id]; ok {
		return d.Code
	}
	return fmt.Sprintf("%d", id)
}
