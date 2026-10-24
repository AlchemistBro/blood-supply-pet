package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func patientProgress(rows []Projection, status string) (string, error) {
	if status == "CANCELLED" {
		return "RELEASE_REQUESTED", nil
	}
	acknowledged := map[string]bool{}
	for _, row := range rows {
		if strings.HasPrefix(row.Key, "allocation:") {
			acknowledged[row.Key] = true
		}
	}
	active, pending := false, false
	for _, row := range rows {
		if !strings.HasPrefix(row.Key, "obligation:") {
			continue
		}
		var o obligationProjection
		if err := json.Unmarshal(row.Payload, &o); err != nil {
			return "", err
		}
		if o.AllocationVersion <= 0 || (o.Status != "CONFIRMED" && o.Status != "TRANSPORT_REQUESTED" && o.Status != "DISPATCHED" && o.Status != "DELIVERED") {
			continue
		}
		active = true
		key := uuid.NewSHA1(o.ID, []byte(fmt.Sprintf("allocate:%d", o.AllocationVersion)))
		pending = pending || !acknowledged["allocation:"+key.String()]
	}
	if !active {
		return "NOT_REQUESTED", nil
	}
	if pending {
		return "PENDING", nil
	}
	return "ACKNOWLEDGED", nil
}
