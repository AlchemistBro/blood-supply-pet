package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func TestSixReplacementResults(t *testing.T) {
	o, a, b := uuid.New(), uuid.New(), uuid.New()
	results := []domain.ReplacementResult{
		domain.Replaced{ObligationID: o, OldUnitID: a, NewUnitID: b},
		domain.NotAllowed{Status: domain.StatusPending},
		domain.SpecMismatch{Reason: domain.MismatchBloodGroup},
		domain.UnitUnavailable{UnitID: b},
		domain.UnitTaken{UnitID: b},
		domain.SourceSystemUnavailable{},
	}
	codes := map[string]bool{}
	for _, r := range results {
		if r.Code() == "" || r.Message() == "" {
			t.Errorf("%T: пустой код или текст", r)
		}
		if codes[r.Code()] {
			t.Errorf("%T: код %s повторяется", r, r.Code())
		}
		codes[r.Code()] = true
	}
	if len(domain.ReplacementCodes) != 6 {
		t.Fatalf("вариантов замены %d, ожидали 6", len(domain.ReplacementCodes))
	}
	for _, c := range domain.ReplacementCodes {
		if !codes[c] {
			t.Errorf("нет варианта с кодом %s", c)
		}
	}
}

func ptr[T any](v T) *T { return &v }

var arrival = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func candidate() domain.UnitCopy {
	return domain.UnitCopy{
		UnitID: uuid.New(), ClinicID: 7, ComponentKind: "RBC", BloodGroup: "A+", RhPositive: true,
		Phenotype: ptr("CcDee"), ExpiresAt: arrival.Add(48 * time.Hour), Status: domain.CopyInStock,
	}
}

func fullSpec() domain.ReplacementSpec {
	return domain.ReplacementSpec{
		BloodGroup: ptr("A"), RhPositive: ptr(true), Phenotype: ptr("CcDee"), MinExpiresAt: ptr(arrival), RuleVersion: "O15-v3",
	}
}

// Спецификация из выбора врача (ADR-0011): источник, вид, группа, резус, фенотип, годность на момент прибытия.
func TestReplacementSpecChecksEveryCriterion(t *testing.T) {
	if r := fullSpec().Mismatch(7, "RBC", candidate()); r != "" {
		t.Fatalf("равноценная единица отклонена: %s", r)
	}
	cases := []struct {
		name string
		edit func(*domain.UnitCopy)
		want domain.MismatchReason
	}{
		{"другой источник", func(u *domain.UnitCopy) { u.ClinicID = 8 }, domain.MismatchSource},
		{"другой вид", func(u *domain.UnitCopy) { u.ComponentKind = "FFP" }, domain.MismatchKind},
		{"другая группа", func(u *domain.UnitCopy) { u.BloodGroup = "B+" }, domain.MismatchBloodGroup},
		{"другой резус", func(u *domain.UnitCopy) { u.RhPositive = false; u.BloodGroup = "A-" }, domain.MismatchRh},
		{"другой фенотип", func(u *domain.UnitCopy) { u.Phenotype = ptr("ccdee") }, domain.MismatchPhenotype},
		{"фенотип не известен", func(u *domain.UnitCopy) { u.Phenotype = nil }, domain.MismatchPhenotype},
		{"годность кончится до прибытия", func(u *domain.UnitCopy) { u.ExpiresAt = arrival.Add(-time.Minute) }, domain.MismatchExpiry},
	}
	for _, c := range cases {
		u := candidate()
		c.edit(&u)
		if got := fullSpec().Mismatch(7, "RBC", u); got != c.want {
			t.Errorf("%s: %q, ожидали %q", c.name, got, c.want)
		}
	}
}

func TestExpiryExactlyAtArrivalIsEnough(t *testing.T) {
	u := candidate()
	u.ExpiresAt = arrival
	if r := fullSpec().Mismatch(7, "RBC", u); r != "" {
		t.Errorf("годность ровно на момент прибытия: %s", r)
	}
}

// Пустая спецификация («любая») проверяет только источник и вид.
func TestAnySpecChecksOnlySourceAndKind(t *testing.T) {
	spec := domain.ReplacementSpec{RuleVersion: "O15-v3"}
	u := candidate()
	u.BloodGroup, u.RhPositive, u.Phenotype, u.ExpiresAt = "O-", false, nil, arrival.Add(-time.Hour)
	if r := spec.Mismatch(7, "RBC", u); r != "" {
		t.Errorf("любая спецификация: %s", r)
	}
	if r := spec.Mismatch(8, "RBC", u); r != domain.MismatchSource {
		t.Errorf("источник проверяется всегда: %s", r)
	}
}

// Группа сравнивается по AB0, резус отдельно: «A» и «A+» — одна группа.
func TestBloodGroupComparedByABO(t *testing.T) {
	for _, pair := range [][2]string{{"A", "A+"}, {"A+", "A"}, {"AB", "AB-"}, {"o", "O+"}} {
		spec := domain.ReplacementSpec{BloodGroup: ptr(pair[0]), RuleVersion: "v"}
		u := candidate()
		u.BloodGroup = pair[1]
		if r := spec.Mismatch(7, "RBC", u); r != "" {
			t.Errorf("%s против %s: %s", pair[0], pair[1], r)
		}
	}
	spec := domain.ReplacementSpec{BloodGroup: ptr("A"), RuleVersion: "v"}
	u := candidate()
	u.BloodGroup = "AB+"
	if r := spec.Mismatch(7, "RBC", u); r != domain.MismatchBloodGroup {
		t.Errorf("A против AB+: %s", r)
	}
	// Знак в группе спецификации — требование к резусу, даже если отдельного поля нет.
	spec = domain.ReplacementSpec{BloodGroup: ptr("A+"), RuleVersion: "v"}
	u = candidate()
	u.BloodGroup, u.RhPositive = "A-", false
	if r := spec.Mismatch(7, "RBC", u); r != domain.MismatchRh {
		t.Errorf("A+ против A-: %s", r)
	}
}

// Фенотип — квазиидентификатор: текст причины не содержит значений спецификации.
func TestMismatchReasonDoesNotLeakPhenotype(t *testing.T) {
	u := candidate()
	u.Phenotype = ptr("ccdee")
	r := fullSpec().Mismatch(7, "RBC", u)
	if strings.Contains(string(r), "CcDee") || strings.Contains(r.Text(), "CcDee") || strings.Contains(r.Text(), "ccdee") {
		t.Errorf("причина раскрывает фенотип: %s / %s", r, r.Text())
	}
	res := domain.SpecMismatch{Reason: r}
	if strings.Contains(res.Message(), "CcDee") {
		t.Errorf("сообщение раскрывает фенотип: %s", res.Message())
	}
}
