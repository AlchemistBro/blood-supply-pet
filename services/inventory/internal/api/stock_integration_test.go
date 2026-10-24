//go:build integration

package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth/authtest"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/api/gen"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/fakeaist"
)

func (e env) tokenAt(t *testing.T, role auth.Role, clinic *int64) string {
	t.Helper()
	return e.idp.Token(t, authtest.TokenOptions{Username: strings.ToLower(string(role)), Roles: []auth.Role{role}, ClinicID: clinic})
}

// Просмотр запаса: оператор и врач — только своя клиника, дежурный и ИТ-служба — любая;
// аналитику и служебному токену просмотр запрещён. Каждый отказ пишется в аудит.
func TestStockAccessByRoleAndClinic(t *testing.T) {
	e := setup(t, false)
	e.syncUnit(t, "U-1")
	cases := []struct {
		name   string
		role   auth.Role
		clinic *int64
		path   string
		want   int
	}{
		{"оператор своей клиники", auth.RoleOperator, authtest.Clinic(1), "/clinics/1/stock", http.StatusOK},
		{"оператор чужой клиники", auth.RoleOperator, authtest.Clinic(1), "/clinics/2/stock", http.StatusForbidden},
		{"врач своей клиники", auth.RolePhysician, authtest.Clinic(2), "/clinics/2/units", http.StatusOK},
		{"врач чужой клиники", auth.RolePhysician, authtest.Clinic(2), "/clinics/1/units", http.StatusForbidden},
		{"врач без клиники", auth.RolePhysician, nil, "/clinics/1/stock", http.StatusForbidden},
		{"дежурный, любая клиника", auth.RoleDuty, nil, "/clinics/2/stock", http.StatusOK},
		{"ИТ-служба, любая клиника", auth.RoleITService, nil, "/clinics/1/units", http.StatusOK},
		{"аналитик", auth.RoleAnalyst, nil, "/clinics/1/stock", http.StatusForbidden},
		{"служебный токен", auth.RoleService, nil, "/clinics/1/stock", http.StatusForbidden},
		{"журнал обмена оператору", auth.RoleOperator, authtest.Clinic(3), "/sync/last", http.StatusOK},
		{"журнал обмена аналитику", auth.RoleAnalyst, nil, "/sync/runs", http.StatusForbidden},
	}
	denied := 0
	for _, c := range cases {
		resp := e.do(t, http.MethodGet, c.path, e.tokenAt(t, c.role, c.clinic), nil)
		_ = resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: %s → %d, ожидали %d", c.name, c.path, resp.StatusCode, c.want)
		}
		if c.want == http.StatusForbidden {
			denied++
		}
	}
	got := e.denied.list()
	if len(got) != denied {
		t.Fatalf("отказов в аудите %d, ожидали %d: %v", len(got), denied, got)
	}
	for _, op := range got {
		if op != auth.OpViewStock {
			t.Fatalf("отказ записан с операцией %s, ожидали VIEW_STOCK", op)
		}
	}
	resp := e.do(t, http.MethodGet, "/clinics/1/stock", "", nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("без токена: %d, ожидали 401", resp.StatusCode)
	}
}

// Карточка и история единицы чужой клиники недоступны оператору; исходный код АИСТ-М не отдаётся никому.
func TestUnitCardAccessAndNoAistCode(t *testing.T) {
	e := setup(t, false)
	e.aist.Add(fakeaist.Unit{AistUnitCode: "AIST-CODE-777", ClinicCode: "C002", Phenotype: "ccdee"})
	id := e.syncUnit(t, "U-OWN")
	var foreign uuid.UUID
	if err := e.pool.QueryRow(t.Context(), `SELECT unit_id FROM unit WHERE aist_unit_code = 'AIST-CODE-777'`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	op1 := e.tokenAt(t, auth.RoleOperator, authtest.Clinic(1))
	for _, p := range []string{"/units/" + foreign.String(), "/units/" + foreign.String() + "/history"} {
		resp := e.do(t, http.MethodGet, p, op1, nil)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s оператору клиники 1: %d, ожидали 403", p, resp.StatusCode)
		}
	}
	resp := e.do(t, http.MethodGet, "/units/"+id.String(), op1, nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("своя единица: %d", resp.StatusCode)
	}

	it := e.tokenAt(t, auth.RoleITService, nil)
	for _, p := range []string{"/units/" + foreign.String(), "/units/" + foreign.String() + "/history", "/clinics/2/units"} {
		resp := e.do(t, http.MethodGet, p, it, nil)
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s ИТ-службе: %d", p, resp.StatusCode)
		}
		if strings.Contains(string(body), "AIST-CODE-777") || strings.Contains(string(body), "aist_unit_code") {
			t.Fatalf("%s отдаёт исходный код АИСТ-М (ADR-0010): %s", p, body)
		}
	}
	resp = e.do(t, http.MethodGet, "/units/"+uuid.Must(uuid.NewV7()).String(), it, nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("неизвестная единица: %d, ожидали 404", resp.StatusCode)
	}
	resp = e.do(t, http.MethodGet, "/clinics/99999/stock", it, nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("неизвестная клиника: %d, ожидали 404", resp.StatusCode)
	}
}

// Ответы просмотра соответствуют контракту OpenAPI (строгий разбор в сгенерированные типы).
func TestStockResponsesMatchContract(t *testing.T) {
	e := setup(t, false)
	id := e.syncUnit(t, "U-1")
	duty := e.tokenAt(t, auth.RoleDuty, nil)

	var stock gen.ClinicStock
	resp := e.do(t, http.MethodGet, "/clinics/1/stock", duty, nil)
	decodeStrict(t, resp, &stock)
	_ = resp.Body.Close()
	if stock.ClinicId != 1 || len(stock.Lines) != 1 || stock.Lines[0].UnitCount != 1 || stock.Lines[0].VolumeL != 0.3 ||
		stock.Watermark == nil || stock.IsStale {
		t.Fatalf("запас: %+v", stock)
	}

	var page gen.UnitPage
	resp = e.do(t, http.MethodGet, "/clinics/1/units?fefo=true&kind=RBC&group=A%2B&limit=10", duty, nil)
	decodeStrict(t, resp, &page)
	_ = resp.Body.Close()
	if len(page.Units) != 1 || uuid.UUID(page.Units[0].UnitId) != id || !page.Units[0].Available || !page.Units[0].RhPositive {
		t.Fatalf("единицы: %+v", page)
	}
	resp = e.do(t, http.MethodGet, "/clinics/1/units?kind=BLOOD", duty, nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("неизвестный вид: %d, ожидали 400", resp.StatusCode)
	}

	var card gen.UnitCard
	resp = e.do(t, http.MethodGet, "/units/"+id.String(), duty, nil)
	decodeStrict(t, resp, &card)
	_ = resp.Body.Close()
	if card.Status != "IN_STOCK" || card.Version != 1 {
		t.Fatalf("карточка: %+v", card)
	}

	var history gen.UnitHistory
	resp = e.do(t, http.MethodGet, "/units/"+id.String()+"/history", duty, nil)
	decodeStrict(t, resp, &history)
	_ = resp.Body.Close()
	if len(history.Items) != 1 || history.Items[0].Status != "IN_STOCK" {
		t.Fatalf("история: %+v", history)
	}

	var last gen.SyncOverview
	resp = e.do(t, http.MethodGet, "/sync/last", duty, nil)
	decodeStrict(t, resp, &last)
	_ = resp.Body.Close()
	if last.LastSuccess == nil || last.LastRun == nil || last.Watermark == nil {
		t.Fatalf("последний обмен: %+v", last)
	}

	var runs gen.SyncRunPage
	resp = e.do(t, http.MethodGet, "/sync/runs?kind=CHANGES&limit=1", duty, nil)
	decodeStrict(t, resp, &runs)
	_ = resp.Body.Close()
	if len(runs.Items) != 1 || runs.Items[0].SyncKind != "CHANGES" {
		t.Fatalf("журнал: %+v", runs)
	}
}
