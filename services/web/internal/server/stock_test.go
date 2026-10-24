package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/svcclient"
)

func stockEnv(t *testing.T) (*env, *int) {
	t.Helper()
	e, _ := operatorEnv(t, 200, "Готово")
	writes := new(int)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			*writes++
			_, _ = w.Write([]byte(`{"component_kind":"RBC","result":"SUCCESS","applied":1}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/norms") {
			_, _ = w.Write([]byte(`[{"component_kind":"RBC","norm_ml":1000}]`))
			return
		}
		_, _ = w.Write([]byte(`{"lines":[]}`))
	}))
	t.Cleanup(upstream.Close)
	e.srv.inventory = svcclient.New("inventory", upstream.URL)
	return e, writes
}

func TestStockShowsBelowNormEvenWhenNoUnitsRemain(t *testing.T) {
	e, _ := stockEnv(t)
	j := e.login(operator, "/stock")
	rec := e.do(http.MethodGet, "/stock", j, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Ниже норматива") {
		t.Fatalf("нулевой запас: %d %s", rec.Code, rec.Body.String())
	}
}

func TestStockConsumptionFormWorksWithoutJavaScript(t *testing.T) {
	e, writes := stockEnv(t)
	j := e.login(operator, "/stock")
	rec := e.do(http.MethodPost, "/stock/annual-consumption", j, postForm(url.Values{"csrf_token": {csrfOf(t, e, j)}, "clinic": {"1"}, "component_kind": {"RBC"}, "consumption_year": {"2026"}, "annual_volume_ml": {"360000"}}))
	if rec.Code != 200 || *writes != 1 {
		t.Fatalf("сохранение расхода: %d, вызовов %d", rec.Code, *writes)
	}
}

func TestStockCannotUpdateAnotherClinic(t *testing.T) {
	e, writes := stockEnv(t)
	j := e.login(operator, "/stock")
	rec := e.do(http.MethodPost, "/stock/sync", j, postForm(url.Values{"csrf_token": {csrfOf(t, e, j)}, "clinic": {"2"}}))
	if rec.Code != 403 || *writes != 0 {
		t.Fatalf("обновление чужой клиники: %d", rec.Code)
	}
}
