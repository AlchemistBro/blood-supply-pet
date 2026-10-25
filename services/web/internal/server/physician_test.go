package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/svcclient"
)

func TestPhysicianListUsesCompactRequestSummary(t *testing.T) {
	e := newEnv(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("summary") == "true" {
			_, _ = io.WriteString(w, `[{"clinical_request_id":"`+taskID+`","status":"OFFERS_READY","variants":[]}]`)
			return
		}
		// Полный список с вариантами превышает лимит ответа BFF в 8 МиБ.
		_, _ = io.WriteString(w, `[{"variants":[{"explanation":"`+strings.Repeat("x", 9<<20)+`"}]}]`)
	}))
	defer upstream.Close()
	e.srv.matching = svcclient.New("matching", upstream.URL)
	mux := http.NewServeMux()
	e.srv.Register(mux)
	e.handler = httpx.Chain(mux, e.srv.Middleware()...)
	j := e.login(physician, "/requests")
	response := e.do(http.MethodGet, "/requests", j, nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), taskID) {
		t.Fatalf("список должен загружаться без графа вариантов: HTTP %d", response.Code)
	}
}

func TestPhysicianScreensKeepCaseSeparateAndRejectUnprotectedPost(t *testing.T) {
	e := newEnv(t)
	writes := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("нет токена пользователя")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			writes++
		}
		switch r.URL.Path {
		case "/requests", "/rules":
			_, _ = io.WriteString(w, `[]`)
		case "/requests/" + taskID:
			_, _ = io.WriteString(w, `{"clinical_request_id":"`+taskID+`","status":"OFFERS_READY","variants":[{"variant_offer_id":"`+unitID+`","rank_no":1,"source_clinic_id":1,"is_local":true,"explanation":"demo","unit_ids":[],"phenotype":"SECRET-PHENOTYPE"}],"patient_case_no":"SECRET-CASE"}`)
		case "/requests/" + taskID + "/patient-case":
			_, _ = io.WriteString(w, `{"patient_case_no":"SECRET-CASE"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	e.srv.matching = svcclient.New("matching", upstream.URL)
	mux := http.NewServeMux()
	e.srv.Register(mux)
	e.handler = httpx.Chain(mux, e.srv.Middleware()...)
	j := e.login(physician, "/requests")
	for _, path := range []string{"/requests", "/requests/new", "/requests/" + taskID, "/rules"} {
		response := e.do(http.MethodGet, path, j, nil)
		if response.Code != 200 {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "SECRET-CASE") || strings.Contains(response.Body.String(), "SECRET-PHENOTYPE") {
			t.Fatal("K1 попал в обычный экран")
		}
	}
	response := e.do(http.MethodGet, "/requests/"+taskID+"/patient-case", j, nil)
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), "SECRET-CASE") {
		t.Fatalf("отдельный показ номера: %d", response.Code)
	}
	response = e.do(http.MethodPost, "/requests", j, nil)
	if response.Code != 403 || writes != 0 {
		t.Fatal("форма без CSRF отправлена в matching")
	}
}
