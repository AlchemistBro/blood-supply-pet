package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/svcclient"
)

const taskID = "01920000-0000-7000-8000-000000000001"
const unitID = "01920000-0000-7000-8000-000000000002"

func operatorEnv(t *testing.T, confirmCode int, message string) (*env, *int) {
	t.Helper()
	e := newEnv(t)
	calls := new(int)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("запрос без токена пользователя")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			*calls++
			var body struct {
				Units []string `json:"unit_ids"`
				Key   string   `json:"idempotency_key"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Units) != 1 || body.Units[0] != unitID || body.Key != "form-key" {
				t.Errorf("неверное подтверждение: %+v, %v", body, err)
			}
			w.WriteHeader(confirmCode)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "CONFIRMED", "message": message})
		case strings.HasSuffix(r.URL.Path, "/units"):
			_, _ = w.Write([]byte(`{"units":[{"unit_id":"` + unitID + `","available":true,"component_kind":"RBC","blood_group":"O+","volume_ml":300,"phenotype":"SECRET-PHENOTYPE"}]}`))
		case r.URL.Path == "/sync/last":
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{"obligation_id":"` + taskID + `","source_clinic_id":1,"target_clinic_id":2,"component_kind":"RBC","requested_qty":1,"status":"PENDING","spec":{"phenotype_required":true,"phenotype":"SECRET-PHENOTYPE"}}`))
		}
	}))
	t.Cleanup(upstream.Close)
	e.srv.transfer = svcclient.New("transfer", upstream.URL)
	e.srv.inventory = svcclient.New("inventory", upstream.URL)
	mux := http.NewServeMux()
	e.srv.Register(mux)
	e.handler = http.Handler(mux)
	for i := len(e.srv.Middleware()) - 1; i >= 0; i-- {
		e.handler = e.srv.Middleware()[i](e.handler)
	}
	return e, calls
}

func TestOperatorCardDoesNotExposePhenotype(t *testing.T) {
	e, _ := operatorEnv(t, http.StatusOK, "Закреплено")
	j := e.login(operator, "/tasks/"+taskID)
	rec := e.do(http.MethodGet, "/tasks/"+taskID, j, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), unitID) || strings.Contains(rec.Body.String(), "SECRET-PHENOTYPE") {
		t.Fatalf("карточка задания: %d %s", rec.Code, rec.Body.String())
	}
}

func TestOperatorConfirmWorksWithoutJavaScript(t *testing.T) {
	for _, status := range []int{200, 409, 422, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			e, calls := operatorEnv(t, status, "Результат подтверждения")
			j := e.login(operator, "/tasks/"+taskID)
			rec := e.do(http.MethodPost, "/tasks/"+taskID+"/confirm", j, postForm(url.Values{
				"csrf_token": {csrfOf(t, e, j)}, "unit_ids": {unitID}, "idempotency_key": {"form-key"},
			}))
			if rec.Code != status || *calls != 1 || !strings.Contains(rec.Body.String(), "Результат подтверждения") {
				t.Fatalf("ответ формы: %d, вызовов %d, %s", rec.Code, *calls, rec.Body.String())
			}
		})
	}
}

func TestOperatorConfirmRejectsForeignRoleAndMissingCSRF(t *testing.T) {
	e, calls := operatorEnv(t, http.StatusOK, "Закреплено")
	j := e.login(physician, "/")
	rec := e.do(http.MethodPost, "/tasks/"+taskID+"/confirm", j, postForm(url.Values{"csrf_token": {csrfOf(t, e, j)}, "unit_ids": {unitID}, "idempotency_key": {"form-key"}}))
	if rec.Code != http.StatusForbidden || *calls != 0 {
		t.Fatalf("врач подтвердил задание: %d", rec.Code)
	}
	j = e.login(operator, "/")
	rec = e.do(http.MethodPost, "/tasks/"+taskID+"/confirm", j, postForm(url.Values{"unit_ids": {unitID}}))
	if rec.Code != http.StatusForbidden || *calls != 0 {
		t.Fatalf("форма без CSRF принята: %d", rec.Code)
	}
}
