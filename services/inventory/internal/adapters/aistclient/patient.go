package aistclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
)

func (c *Client) AllocatePatient(ctx context.Context, key uuid.UUID, plain, clinic string, codes []string) (time.Time, error) {
	// plain не покидает тело POST; из ответов C01 принимаются только квитанция и ключ.
	raw, err := json.Marshal(struct {
		Key     string   `json:"idempotency_key"`
		Patient string   `json:"patient_case_no"`
		Clinic  string   `json:"recipient_clinic_code"`
		Codes   []string `json:"aist_unit_codes"`
	}{key.String(), plain, clinic, codes})
	if err != nil {
		return time.Time{}, fmt.Errorf("не удалось подготовить ИНТ-05")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/allocations", bytes.NewReader(raw))
	if err != nil {
		return time.Time{}, fmt.Errorf("не удалось создать запрос ИНТ-05")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key.String())
	resp, err := c.status.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("АИСТ-М не ответила на ИНТ-05")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return time.Time{}, fmt.Errorf("АИСТ-М отклонила ИНТ-05: HTTP %d", resp.StatusCode)
	}
	var ack struct {
		Key      string    `json:"idempotency_key"`
		At       time.Time `json:"acknowledged_at"`
		Released bool      `json:"released"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&ack); err != nil || ack.Key != key.String() || ack.At.IsZero() || ack.Released {
		return time.Time{}, fmt.Errorf("АИСТ-М вернула неверную квитанцию ИНТ-05")
	}
	return ack.At, nil
}
func (c *Client) ReleasePatient(ctx context.Context, allocationKey, commandKey uuid.UUID) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/allocations/"+allocationKey.String(), nil)
	if err != nil {
		return fmt.Errorf("не удалось создать снятие ИНТ-05")
	}
	req.Header.Set("Idempotency-Key", commandKey.String())
	resp, err := c.status.Do(req)
	if err != nil {
		return fmt.Errorf("АИСТ-М не ответила на снятие ИНТ-05")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 204 && resp.StatusCode != 200 {
		return fmt.Errorf("АИСТ-М отклонила снятие ИНТ-05: HTTP %d", resp.StatusCode)
	}
	return nil
}
