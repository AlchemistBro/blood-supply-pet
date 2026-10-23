package domain

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// testGroups — порядок групп как в libs/refdata (демо-справочник, ADR-0010).
var testGroups = []string{"O+", "O-", "A+", "A-", "B+", "B-", "AB+", "AB-"}

// testRef — небольшой справочник: районы D01–D06, клиники C001–C008, виды как в libs/refdata
// (CRYO без признака применимости группы).
func testRef(t testing.TB) *Reference {
	t.Helper()
	var districts []DistrictRef
	for i := int64(1); i <= 6; i++ {
		districts = append(districts, DistrictRef{ID: i, Code: fmt.Sprintf("D%02d", i)})
	}
	var clinics []ClinicRef
	for i := int64(1); i <= 8; i++ {
		clinics = append(clinics, ClinicRef{ID: i, Code: fmt.Sprintf("C%03d", i)})
	}
	kinds := []KindRef{
		{Code: "RBC", IsGroupApplicable: true},
		{Code: "FFP", IsGroupApplicable: true},
		{Code: "PLT", IsGroupApplicable: true},
		{Code: "CRYO", IsGroupApplicable: false},
	}
	ref, err := NewReference(districts, clinics, kinds, testGroups)
	if err != nil {
		t.Fatalf("справочник: %v", err)
	}
	return ref
}

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func period(t testing.TB, start, end time.Time) Period {
	t.Helper()
	p, err := NewPeriod(start, end)
	if err != nil {
		t.Fatalf("период: %v", err)
	}
	return p
}

// csvOf склеивает строки файла через перевод строки.
func csvOf(lines ...string) []byte {
	return []byte(strings.Join(lines, "\n") + "\n")
}

// findRowError ищет ошибку строки по номеру и полю.
func findRowError(errs []RowError, row int, field string) (RowError, bool) {
	for _, e := range errs {
		if e.Row == row && e.Field == field {
			return e, true
		}
	}
	return RowError{}, false
}

func requireRowError(t *testing.T, c Check, row int, field, contains string) {
	t.Helper()
	e, ok := findRowError(c.RowErrors, row, field)
	if !ok {
		t.Fatalf("нет ошибки строки %d поля %q; ошибки: %+v", row, field, c.RowErrors)
	}
	if !strings.Contains(e.Message, contains) {
		t.Fatalf("строка %d поле %q: сообщение %q не содержит %q", row, field, e.Message, contains)
	}
}
