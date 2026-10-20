//go:build e2e

// Package e2e — сквозные сценарии против поднятого стенда (task up).
// Адреса берутся из переменных BS_E2E_*; по умолчанию это порты compose на localhost.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

var (
	keycloakURL = env("BS_E2E_KEYCLOAK", "http://localhost:8180")
	auditURL    = env("BS_E2E_AUDIT", "http://localhost:8108")
	kafkaBroker = env("BS_E2E_KAFKA", "localhost:9092")
)

type tokenResponse struct {
	AccessToken string `json:"access_token"`
}

// login получает токен демо-пользователя по паролю (клиент blood-supply-cli, только для тестов).
func login(t *testing.T, username string) string {
	t.Helper()
	form := url.Values{"grant_type": {"password"}, "client_id": {"blood-supply-cli"}, "username": {username}, "password": {"demo"}}
	resp, err := http.PostForm(keycloakURL+"/realms/blood-supply/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatalf("Keycloak недоступен: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("вход %s: %d %s", username, resp.StatusCode, body)
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil || tr.AccessToken == "" {
		t.Fatalf("токен не получен: %v %s", err, body)
	}
	return tr.AccessToken
}

// serviceToken — токен служебного клиента (client credentials), роль SERVICE.
func serviceToken(t *testing.T) string {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {"services"}, "client_secret": {"services-demo-secret"}}
	resp, err := http.PostForm(keycloakURL+"/realms/blood-supply/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("служебный токен: %d %s", resp.StatusCode, body)
	}
	var tr tokenResponse
	_ = json.Unmarshal(body, &tr)
	return tr.AccessToken
}

func call(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, auditURL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// Токены Keycloak несут роль и клинику, сервис их проверяет, а права разграничены по матрице.
func TestAuthRolesAgainstAuditService(t *testing.T) {
	opToken := login(t, "operator-c001")
	dutyToken := login(t, "duty1")

	if code, _ := call(t, http.MethodGet, "/records", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("без токена: %d, ожидали 401", code)
	}
	if code, _ := call(t, http.MethodGet, "/records", "garbage", nil); code != http.StatusUnauthorized {
		t.Fatalf("мусорный токен: %d, ожидали 401", code)
	}
	if code, body := call(t, http.MethodGet, "/records", opToken, nil); code != http.StatusForbidden {
		t.Fatalf("оператор читает журнал: %d %s, ожидали 403", code, body)
	}
	if code, body := call(t, http.MethodGet, "/records?limit=5", dutyToken, nil); code != http.StatusOK {
		t.Fatalf("дежурный читает журнал: %d %s", code, body)
	}
	if code, _ := call(t, http.MethodGet, "/healthz", "", nil); code != http.StatusOK {
		t.Fatalf("healthz без токена: %d", code)
	}
	if code, body := call(t, http.MethodGet, "/readyz", "", nil); code != http.StatusOK {
		t.Fatalf("readyz: %d %s", code, body)
	}
	if code, body := call(t, http.MethodGet, "/openapi.yaml", "", nil); code != http.StatusOK || !strings.Contains(string(body), "openapi: 3.0.3") {
		t.Fatalf("openapi.yaml: %d", code)
	}
}

// Событие аудита, опубликованное в Kafka, оказывается в журнале и входит в целую цепочку хешей.
func TestAuditEventFlowsThroughKafkaIntoChain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	objectID := "e2e-" + uuid.NewString()

	rec := auditc.AuditRecorded{
		OccurredAt: time.Now().UTC(), UserID: "operator-c001", Role: "CLINIC_OPERATOR", Module: auditc.ModuleTransfer,
		Action: "CONFIRM_TRANSFER", ObjectType: "e2e", ObjectID: objectID, Result: auditc.ResultSuccess,
		Details: map[string]any{"confirmed_qty": 2},
	}
	body, _ := json.Marshal(rec)
	msgID := uuid.New()
	producer, err := messaging.NewProducerClient([]string{kafkaBroker})
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	msg := messaging.Message{ID: msgID, Type: rec.MessageType(), SchemaVersion: 1, Topic: contracts.TopicAuditEvents, Key: "transfer",
		Payload: body, Producer: "e2e", OccurredAt: time.Now().UTC(), CorrelationID: "e2e-corr"}
	// Дважды: вторая доставка того же message-id должна быть отброшена inbox сервиса audit.
	for i := 0; i < 2; i++ {
		if err := producer.ProduceSync(ctx, msg.Record()).FirstErr(); err != nil {
			t.Fatalf("публикация в Kafka: %v", err)
		}
	}

	dutyToken := login(t, "duty1")
	var items []struct {
		Seq      int64  `json:"seq"`
		ObjectID string `json:"object_id"`
		Producer string `json:"producer"`
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		code, out := call(t, http.MethodGet, "/records?objectType=e2e&objectId="+objectID, dutyToken, nil)
		if code != http.StatusOK {
			t.Fatalf("чтение журнала: %d %s", code, out)
		}
		var page struct {
			Items []struct {
				Seq      int64  `json:"seq"`
				ObjectID string `json:"object_id"`
				Producer string `json:"producer"`
			} `json:"items"`
		}
		_ = json.Unmarshal(out, &page)
		items = page.Items
		if len(items) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(items) != 1 {
		t.Fatalf("в журнале %d записей по объекту %s, ожидали ровно 1 (дубль доставки должен быть отброшен)", len(items), objectID)
	}
	if items[0].Producer != "transfer" && items[0].Producer != "e2e" {
		t.Fatalf("producer записи: %q", items[0].Producer)
	}

	code, out := call(t, http.MethodGet, "/internal/verify", dutyToken, nil)
	if code != http.StatusOK {
		t.Fatalf("verify: %d %s", code, out)
	}
	var vr struct {
		OK      bool  `json:"ok"`
		Checked int64 `json:"checked"`
	}
	_ = json.Unmarshal(out, &vr)
	if !vr.OK || vr.Checked < 1 {
		t.Fatalf("цепочка аудита нарушена или пуста: %s", out)
	}
}

// web не имеет своей базы и пишет аудит по HTTP служебным токеном (ADR-0017); обычный пользователь так писать не может.
func TestAppendRecordOverHTTPRequiresServiceRole(t *testing.T) {
	in := map[string]any{
		"occurred_at": time.Now().UTC().Format(time.RFC3339), "module": "web", "action": "READ_PATIENT_CASE",
		"object_type": "clinical_request", "object_id": "r-" + uuid.NewString(), "result": "SUCCESS", "user_id": "physician1",
	}
	if code, _ := call(t, http.MethodPost, "/records", login(t, "physician1"), in); code != http.StatusForbidden {
		t.Fatalf("врач пишет в журнал напрямую: %d, ожидали 403", code)
	}
	code, out := call(t, http.MethodPost, "/records", serviceToken(t), in)
	if code != http.StatusCreated {
		t.Fatalf("служебная запись: %d %s", code, out)
	}
	// Запрещённое поле в details отклоняется (NFR016: номер случая в журнал не попадает).
	in["details"] = map[string]any{"patient_case_no": "CASE-1"}
	if code, _ := call(t, http.MethodPost, "/records", serviceToken(t), in); code != http.StatusBadRequest {
		t.Fatalf("запись с номером случая в details: %d, ожидали 400", code)
	}
	_ = fmt.Sprint()
}
