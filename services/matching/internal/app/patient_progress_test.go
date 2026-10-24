package app

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func TestOldPatientAcknowledgementDoesNotConfirmReplacement(t *testing.T) {
	id := uuid.New()
	o := obligationProjection{ID: id, Status: "CONFIRMED", Version: 3, AllocationVersion: 3}
	raw, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	old := uuid.NewSHA1(id, []byte("allocate:2"))
	current := uuid.NewSHA1(id, []byte("allocate:3"))
	rows := []Projection{{Key: "obligation:" + id.String(), Payload: raw}, {Key: "allocation:" + old.String()}}
	if status, err := patientProgress(rows, "CONFIRMED"); err != nil || status != "PENDING" {
		t.Fatalf("старая квитанция подтвердила замену: %s %v", status, err)
	}
	rows = append(rows, Projection{Key: fmt.Sprintf("allocation:%s", current.String())})
	if status, err := patientProgress(rows, "CONFIRMED"); err != nil || status != "ACKNOWLEDGED" {
		t.Fatalf("новая квитанция потеряна: %s %v", status, err)
	}
}
