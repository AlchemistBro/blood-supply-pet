package server

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/oidc/oidctest"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/svcclient"
)

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
