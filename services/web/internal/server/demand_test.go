package server

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/oidc/oidctest"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/svcclient"
)

func TestAnalystReadsSavedErrorsAndRejectsEstimateWithCSRF(t *testing.T) {
	e := newEnv(t)
	writes := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			writes++
			w.WriteHeader(200)
			_, _ = io.WriteString(w, `{}`)
			return
		}
		switch r.URL.Path {
		case "/files/" + taskID:
			_, _ = io.WriteString(w, `{"id":"`+taskID+`","file_name":"history.csv","file_kind":"CONSUMPTION_HISTORY","check_status":"PARTIAL","accepted_rows":1,"rejected_rows":1}`)
		case "/files/" + taskID + "/errors":
			_, _ = io.WriteString(w, `{"items":[{"row_no":3,"reason":"неверный период"}]}`)
		default:
			_, _ = io.WriteString(w, `{"demand_estimate_id":"`+taskID+`","status":"CALCULATED","lines":[],"history_ids":[]}`)
		}
	}))
	defer upstream.Close()
	e.srv.demand = svcclient.New("demand", upstream.URL)
	mux := http.NewServeMux()
	e.srv.Register(mux)
	e.handler = httpx.Chain(mux, e.srv.Middleware()...)
	j := e.login(oidctest.User{Username: "analyst1", Roles: []auth.Role{auth.RoleAnalyst}}, "/demand")
	response := e.do(http.MethodGet, "/demand/files/"+taskID, j, nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "неверный период") {
		t.Fatalf("история ошибок: %d %s", response.Code, response.Body.String())
	}
	response = e.do(http.MethodPost, "/demand/estimates/"+taskID+"/reject", j, postForm(url.Values{"reason": {"исправить входы"}}))
	if response.Code != 403 || writes != 0 {
		t.Fatal("отклонение без CSRF дошло до сервиса")
	}
	response = e.do(http.MethodPost, "/demand/estimates/"+taskID+"/reject", j, postForm(url.Values{"reason": {"исправить входы"}, "csrf_token": {csrfOf(t, e, j)}}))
	if response.Code != 303 || writes != 1 {
		t.Fatalf("отклонение: %d %s", response.Code, response.Body.String())
	}
	for kind := range demandTemplates {
		response = e.do(http.MethodGet, "/demand/templates/"+kind, j, nil)
		if response.Code != 200 || !strings.Contains(response.Header().Get("Content-Type"), "text/csv") {
			t.Fatalf("нет шаблона %s", kind)
		}
	}
}

func TestAnalystUploadsCSVWithoutJavaScriptAndWithCSRF(t *testing.T) {
	e := newEnv(t)
	uploads := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			uploads++
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			if r.MultipartForm != nil {
				defer func() { _ = r.MultipartForm.RemoveAll() }()
			}
			if r.FormValue("file_kind") != "DISTRICT_PROFILE" {
				t.Error("вид файла не передан")
			}
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"file":{"check_status":"ACCEPTED","accepted_rows":1,"rejected_rows":0}}`)
			return
		}
		if r.URL.Path == "/estimates" {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		_, _ = io.WriteString(w, `{"items":[]}`)
	}))
	defer upstream.Close()
	e.srv.demand = svcclient.New("demand", upstream.URL)
	mux := http.NewServeMux()
	e.srv.Register(mux)
	e.handler = httpx.Chain(mux, e.srv.Middleware()...)
	j := e.login(oidctest.User{Username: "analyst1", Roles: []auth.Role{auth.RoleAnalyst}}, "/demand")
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("csrf_token", csrfOf(t, e, j)); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("file_kind", "DISTRICT_PROFILE"); err != nil {
		t.Fatal(err)
	}
	part, err := mw.CreateFormFile("file", "sample.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "header\nvalue\n"); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	rec := e.do(http.MethodPost, "/demand/files", j, func(r *http.Request) {
		r.Header.Set("Content-Type", mw.FormDataContentType())
		r.Body = io.NopCloser(bytes.NewReader(buf.Bytes()))
	})
	if rec.Code != 200 || uploads != 1 || !strings.Contains(rec.Body.String(), "Файл проверен") {
		t.Fatalf("загрузка: %d, вызовов %d, %s", rec.Code, uploads, rec.Body.String())
	}
}

// Отсутствующие входы не должны оставлять пустой обязательный select и доступную кнопку расчёта.

func TestDemandCalculationExplainsMissingInputs(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing shares", true: "ready"}[ready], func(t *testing.T) {
			e := newEnv(t)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/estimates":
					_, _ = io.WriteString(w, `[]`)
				case "/files":
					_, _ = io.WriteString(w, `{"items":[{"id":"`+taskID+`","file_name":"profile.csv","file_kind":"DISTRICT_PROFILE","check_status":"ACCEPTED"}]}`)
				case "/history":
					_, _ = io.WriteString(w, `{"items":[{"id":"`+unitID+`","component_kind_code":"RBC","period_start":"2025-01-01","period_end":"2025-12-31","consumed_volume_ml":300}]}`)
				case "/shares/versions":
					if ready {
						_, _ = io.WriteString(w, `{"items":[{"id":"`+taskID+`","version_no":3}]}`)
					} else {
						_, _ = io.WriteString(w, `{"items":[]}`)
					}
				}
			}))
			t.Cleanup(upstream.Close)
			e.srv.demand = svcclient.New("demand", upstream.URL)
			mux := http.NewServeMux()
			e.srv.Register(mux)
			e.handler = httpx.Chain(mux, e.srv.Middleware()...)
			j := e.login(oidctest.User{Username: "analyst1", Roles: []auth.Role{auth.RoleAnalyst}}, "/demand")
			rec := e.do(http.MethodGet, "/demand", j, nil)
			body := rec.Body.String()
			if rec.Code != http.StatusOK {
				t.Fatalf("страница: %d", rec.Code)
			}
			if ready {
				if !strings.Contains(body, `name="clinic_share_version_id"`) || !strings.Contains(body, "Версия 3") || strings.Contains(body, `type="submit" disabled`) {
					t.Fatal("загруженные входы не открыли расчёт")
				}
				return
			}
			if !strings.Contains(body, "Для расчёта не хватает входных данных") || !strings.Contains(body, "Доли район → клиника") || !strings.Contains(body, `type="submit" disabled`) {
				t.Fatal("нет объяснения отсутствующих долей и блокировки расчёта")
			}
			if strings.Contains(body, `name="clinic_share_version_id"`) {
				t.Fatal("показан пустой обязательный список")
			}
		})
	}
}
