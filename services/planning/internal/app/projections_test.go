package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
)

func TestUnitProjectionDropsPhenotypeAndSourceCode(t *testing.T) {
	key, version, raw, err := projectionMessage(messaging.Message{Type: "UnitRegistered", Payload: json.RawMessage(`{"unit_id":"01920000-0000-7000-8000-000000000001","clinic_id":1,"component_kind":"RBC","blood_group":"O+","volume_ml":300,"status":"IN_STOCK","version":7,"phenotype":"SECRET","aist_unit_code":"SOURCE"}`)})
	if err != nil || version != 7 || !strings.HasPrefix(key, "unit:") {
		t.Fatalf("проекция: %s %d %v", key, version, err)
	}
	if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "SOURCE") || strings.Contains(string(raw), "phenotype") {
		t.Fatalf("лишние поля в проекции: %s", raw)
	}
}
