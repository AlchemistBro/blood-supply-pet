package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/notification/api"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/domain"
)

// roleVerifier — токен равен имени роли.
type roleVerifier struct{}

func (roleVerifier) Verify(_ context.Context, raw string) (auth.Principal, error) {
	return auth.Principal{Subject: "u-" + raw, Username: "user-" + strings.ToLower(raw), Roles: []auth.Role{auth.Role(raw)}}, nil
}

type fakeLister struct {
	got   app.ListFilter
	items []domain.Notification
}

func (f *fakeLister) List(_ context.Context, filter app.ListFilter) ([]domain.Notification, error) {
	f.got = filter
	return f.items, nil
}

func sample() []domain.Notification {
	clinic := int64(1)
	sent := time.Date(2026, 10, 23, 10, 0, 5, 0, time.UTC)
	qty := 3
	return []domain.Notification{
		{
			ID: uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-400000000001"), Kind: domain.KindUrgentTask,
			SourceMessageID: uuid.New(), To: domain.Recipient{Role: domain.RoleOperator, ClinicID: &clinic},
			Email: "transfusion.c001@demo.blood-supply.local", Subject: "[демо] Срочное задание на передачу № 0192f3a1",
			Facts: domain.Facts{
				ObligationID: uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-300000000001"),
				RequestID:    uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-200000000001"), Quantity: &qty,
			},
			Status: domain.StatusSent, Attempts: 1, NextAttemptAt: sent, CreatedAt: sent.Add(-5 * time.Second), SentAt: &sent,
		},
		{
			ID: uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-400000000002"), Kind: domain.KindEscalation,
			SourceMessageID: uuid.New(), To: domain.Recipient{Role: domain.RoleDuty},
			Subject: "[демо] Эскалация по заданию № 0192f3a1",
			Facts:   domain.Facts{ObligationID: uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-300000000001")},
			Status:  domain.StatusFailed, Attempts: 0, NextAttemptAt: sent, LastError: domain.ErrNoRecipient, CreatedAt: sent,
		},
	}
}

func server(t *testing.T, l api.Lister) (http.Handler, *[]auth.Operation) {
	t.Helper()
	mux := http.NewServeMux()
	var denied []auth.Operation
	api.Mount(mux, l, func(_ *http.Request, _ *auth.Principal, op auth.Operation) { denied = append(denied, op) })
	return auth.Authenticate(roleVerifier{})(mux), &denied
}

func get(h http.Handler, target, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Журнал писем видят ИТ-служба и дежурный (READ_AUDIT); остальные роли получают 403 с записью отказа.
func TestListRequiresReadAudit(t *testing.T) {
	h, denied := server(t, &fakeLister{items: sample()})
	for _, role := range []auth.Role{auth.RoleITService, auth.RoleDuty} {
		if rec := get(h, "/notifications", string(role)); rec.Code != http.StatusOK {
			t.Errorf("%s: код %d, ожидался 200", role, rec.Code)
		}
	}
	for _, role := range []auth.Role{auth.RolePhysician, auth.RoleOperator, auth.RoleAnalyst, auth.RoleService} {
		if rec := get(h, "/notifications", string(role)); rec.Code != http.StatusForbidden {
			t.Errorf("%s: код %d, ожидался 403", role, rec.Code)
		}
	}
	if len(*denied) != 4 || (*denied)[0] != auth.OpReadAudit {
		t.Fatalf("отказы для аудита: %v", *denied)
	}
	if rec := get(h, "/notifications", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("без токена: код %d, ожидался 401", rec.Code)
	}
}

// Фильтр статуса и предел передаются в приложение; неизвестный статус — 400.
func TestListPassesFilter(t *testing.T) {
	l := &fakeLister{}
	h, _ := server(t, l)
	if rec := get(h, "/notifications?status=FAILED&limit=5", string(auth.RoleITService)); rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body)
	}
	if l.got.Status == nil || *l.got.Status != domain.StatusFailed || l.got.Limit != 5 {
		t.Fatalf("фильтр %+v", l.got)
	}
	if rec := get(h, "/notifications?status=LOST", string(auth.RoleITService)); rec.Code != http.StatusBadRequest {
		t.Fatalf("неизвестный статус: код %d, ожидался 400", rec.Code)
	}
}

// Ответ соответствует контракту api/openapi.yaml.
func TestListResponseMatchesOpenAPI(t *testing.T) {
	ctx := context.Background()
	doc, err := openapi3.NewLoader().LoadFromData(apispec.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(ctx); err != nil {
		t.Fatalf("openapi.yaml невалиден: %v", err)
	}
	path := doc.Paths.Find("/notifications")
	if path == nil || path.Get == nil {
		t.Fatal("в контракте нет GET /notifications")
	}
	if op, _ := path.Get.Extensions["x-bs-operation"].(string); op != string(auth.OpReadAudit) {
		t.Fatalf("x-bs-operation %q, ожидалось %s", op, auth.OpReadAudit)
	}

	h, _ := server(t, &fakeLister{items: sample()})
	for _, tc := range []struct{ target, token string }{
		{"/notifications", string(auth.RoleITService)},
		{"/notifications?status=LOST", string(auth.RoleITService)},
		{"/notifications", string(auth.RolePhysician)},
	} {
		rec := get(h, tc.target, tc.token)
		req := httptest.NewRequest(http.MethodGet, tc.target, nil)
		in := &openapi3filter.ResponseValidationInput{
			RequestValidationInput: &openapi3filter.RequestValidationInput{
				Request: req,
				Route:   &routers.Route{Spec: doc, Path: "/notifications", PathItem: path, Method: http.MethodGet, Operation: path.Get},
			},
			Status: rec.Code,
			Header: rec.Header(),
			Body:   io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
		}
		if err := openapi3filter.ValidateResponse(ctx, in); err != nil {
			t.Errorf("%s (%s): ответ %d не по контракту: %v\n%s", tc.target, tc.token, rec.Code, err, rec.Body)
		}
	}

	rec := get(h, "/notifications", string(auth.RoleITService))
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[1]["recipient_email"] != nil {
		t.Fatalf("ответ %s", rec.Body)
	}
}
