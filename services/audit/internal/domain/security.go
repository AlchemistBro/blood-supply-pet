package domain

import "encoding/json"

// ForeignClinicRead учитывает разрешённое чтение оператором источника (ADR-0025).
func ForeignClinicRead(r Record) bool {
	if r.Action != "READ_PATIENT_CASE" || r.Result != "SUCCESS" {
		return false
	}
	if r.Role == "CLINIC_OPERATOR" && r.Details["access_basis"] == "SOURCE_OBLIGATION" {
		return false
	}
	var object int64
	switch value := r.Details["object_clinic_id"].(type) {
	case int64:
		object = value
	case int:
		object = int64(value)
	case float64:
		object = int64(value)
	case json.Number:
		object, _ = value.Int64()
	}
	return object > 0 && (r.ClinicID == nil || *r.ClinicID != object)
}
