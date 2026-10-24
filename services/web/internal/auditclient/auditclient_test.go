package auditclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/correlation"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

type staticTokens struct {
	token string
	err   error
}

func (s staticTokens) Token(context.Context) (string, error) { return s.token, s.err }

var t0 = time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)

type captured struct {
	auth string
	body auditc.AuditRecorded
	n    int
}

func auditServer(t *testing.T, status int) (*httptest.Server, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/records" {
			t.Errorf("ожидался POST /records, получен %s %s", r.Method, r.URL.Path)
		}
		got.n++
		got.auth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got.body); err != nil {
			t.Errorf("тело записи: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"seq":1,"hash":"00"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

// Отказ в доступе уходит в журнал аудита со служебным токеном, модулем web и участником.
func TestRecordPostsEntryWithServiceToken(t *testing.T) {
	srv, got := auditServer(t, http.StatusCreated)
	c := New(srv.URL, staticTokens{token: "svc-token"}, clock.NewMutable(t0))
	clinic := int64(1)
	p := auth.Principal{Username: "physician1", Roles: []auth.Role{auth.RolePhysician}, ClinicID: &clinic}
	ctx := correlation.With(context.Background(), correlation.IDs{Correlation: "corr-1"})
	err := c.Record(ctx, Entry{
		Action: auditc.ActionAccessDenied, ObjectType: "operation", ObjectID: "CONFIRM_TRANSFER",
		Result: auditc.ResultDenied, Principal: &p, Details: map[string]any{"path": "/tasks/1/confirm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.auth != "Bearer svc-token" {
		t.Fatalf("служебный токен не передан: %q", got.auth)
	}
	b := got.body
	if b.Module != auditc.ModuleWeb || b.Action != "ACCESS_DENIED" || b.Result != "DENIED" ||
		b.UserID != "physician1" || b.Role != "PHYSICIAN" || b.ClinicID == nil || *b.ClinicID != 1 ||
		b.ObjectID != "CONFIRM_TRANSFER" || b.CorrelationID != "corr-1" || !b.OccurredAt.Equal(t0) {
		t.Fatalf("запись аудита: %+v", b)
	}
}

func TestRecordFailsOnNon201(t *testing.T) {
	srv, _ := auditServer(t, http.StatusForbidden)
	c := New(srv.URL, staticTokens{token: "t"}, clock.NewMutable(t0))
	if err := c.Record(context.Background(), Entry{Action: "LOGIN", ObjectType: "session", ObjectID: "u", Result: "SUCCESS"}); err == nil {
		t.Fatal("ответ не 201 — ошибка записи аудита")
	}
}

// Без служебного токена запрос не отправляется.
func TestRecordFailsWithoutToken(t *testing.T) {
	srv, got := auditServer(t, http.StatusCreated)
	c := New(srv.URL, staticTokens{err: errors.New("Keycloak недоступен")}, clock.NewMutable(t0))
	if err := c.Record(context.Background(), Entry{Action: "LOGIN", ObjectType: "session", ObjectID: "u", Result: "SUCCESS"}); err == nil {
		t.Fatal("без токена ожидалась ошибка")
	}
	if got.n != 0 {
		t.Fatal("без токена запрос в audit не отправляется")
	}
}

// ПДн и номер случая в details не уходят даже в журнал (ADR-0017): отказ до отправки.
func TestRecordRejectsForbiddenDetails(t *testing.T) {
	srv, got := auditServer(t, http.StatusCreated)
	c := New(srv.URL, staticTokens{token: "t"}, clock.NewMutable(t0))
	for _, key := range []string{"patient_case_no", "full_name", "snils", "phenotype", "password"} {
		err := c.Record(context.Background(), Entry{Action: "LOGIN", ObjectType: "session", ObjectID: "u", Result: "SUCCESS",
			Details: map[string]any{key: "x"}})
		if err == nil {
			t.Fatalf("ключ %s в details должен отклоняться", key)
		}
	}
	if got.n != 0 {
		t.Fatal("запись с запрещённым ключом не отправляется")
	}
}

func TestLogOnlyNeverFails(t *testing.T) {
	if err := (LogOnly{}).Record(context.Background(), Entry{Action: "LOGIN"}); err != nil {
		t.Fatal(err)
	}
}
