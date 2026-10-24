package domain

import "testing"

func TestForeignClinicAllowsConfirmedSourceOperator(t *testing.T) {
	clinic := int64(1)
	r := Record{Action: "READ_PATIENT_CASE", Result: "SUCCESS", ClinicID: &clinic, Role: "CLINIC_OPERATOR", Details: map[string]any{"object_clinic_id": float64(2), "access_basis": "SOURCE_OBLIGATION"}}
	if ForeignClinicRead(r) {
		t.Fatal("разрешённое чтение оператором признано инцидентом")
	}
	r.Role = "PHYSICIAN"
	if !ForeignClinicRead(r) {
		t.Fatal("основание оператора разрешило чтение врачу")
	}
	r.Details["object_clinic_id"] = float64(1)
	if ForeignClinicRead(r) {
		t.Fatal("собственная клиника признана чужой")
	}
}
