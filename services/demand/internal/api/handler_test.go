package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/demand/api"
)

// fakeVerifier — токен равен роли: «TERRITORIAL_ANALYST», «CLINIC_OPERATOR» и т. д.
type fakeVerifier struct{}

func (fakeVerifier) Verify(_ context.Context, raw string) (auth.Principal, error) {
	if raw == "" || raw == "bad" {
		return auth.Principal{}, auth.ErrUnauthorized
	}
	return auth.Principal{Subject: "u-" + raw, Username: "user-" + strings.ToLower(raw), Roles: []auth.Role{auth.Role(raw)}}, nil
}

type denial struct {
	op   auth.Operation
	user string
}

// server без хранилища: проверки прав и разбор формы срабатывают раньше обращения к сервису.
func server(t *testing.T) (*httptest.Server, *[]denial) {
	t.Helper()
	var denied []denial
	mux := http.NewServeMux()
	Mount(mux, nil, func(_ *http.Request, p *auth.Principal, op auth.Operation) {
		denied = append(denied, denial{op: op, user: p.Username})
	})
	srv := httptest.NewServer(auth.Authenticate(fakeVerifier{})(mux))
	t.Cleanup(srv.Close)
	return srv, &denied
}

type route struct {
	method, pattern, path string
}

var routes = []route{
	{"POST", "/files", "/files"},
	{"GET", "/files", "/files"},
	{"GET", "/files/{id}", "/files/0192f3a1-7b2c-7d4e-8f90-000000000001"},
	{"GET", "/files/{id}/errors", "/files/0192f3a1-7b2c-7d4e-8f90-000000000001/errors"},
	{"GET", "/history", "/history"},
	{"GET", "/shares/versions", "/shares/versions"},
	{"GET", "/shares/current", "/shares/current"},
	{"GET", "/estimates", "/estimates"},
	{"POST", "/estimates", "/estimates"},
	{"GET", "/estimates/{id}", "/estimates/0192f3a1-7b2c-7d4e-8f90-000000000001"},
	{"POST", "/estimates/{id}/publish", "/estimates/0192f3a1-7b2c-7d4e-8f90-000000000001/publish"},
	{"POST", "/estimates/{id}/reject", "/estimates/0192f3a1-7b2c-7d4e-8f90-000000000001/reject"},
}

func multipartBody(t *testing.T, kind, name string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if kind != "" {
		if err := w.WriteField("file_kind", kind); err != nil {
			t.Fatal(err)
		}
	}
	if content != nil {
		fw, err := w.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, w.FormDataContentType()
}

// reply — прочитанный ответ; тело закрыто внутри send.
type reply struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func send(t *testing.T, srv *httptest.Server, method, path, token string, body io.Reader, ctype string) reply {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{StatusCode: resp.StatusCode, Header: resp.Header, Body: b}
}

func do(t *testing.T, srv *httptest.Server, r route, token string) reply {
	t.Helper()
	if r.method == http.MethodPost {
		body, ctype := multipartBody(t, "CLINIC_SHARE", "shares.csv", []byte("district_code\n"))
		return send(t, srv, r.method, r.path, token, body, ctype)
	}
	return send(t, srv, r.method, r.path, token, http.NoBody, "")
}

// Каждый эндпоинт: роль без права получает 403 и запись об отказе (аудит ACCESS_DENIED в сервисе).
func TestEveryRouteForbiddenForRolesWithoutRight(t *testing.T) {
	srv, denied := server(t)
	for _, role := range []auth.Role{auth.RoleOperator, auth.RolePhysician, auth.RoleDuty, auth.RoleITService, auth.RoleService, auth.RoleAnalyst} {
		for _, r := range routes {
			op := operations[r.method+" "+r.pattern]
			if auth.Allowed(op, []auth.Role{role}) {
				continue
			}
			t.Run(string(role)+" "+r.method+" "+r.pattern, func(t *testing.T) {
				before := len(*denied)
				resp := do(t, srv, r, string(role))
				if resp.StatusCode != http.StatusForbidden {
					t.Fatalf("статус %d, ожидали 403", resp.StatusCode)
				}
				if resp.Header.Get("Content-Type") != "application/problem+json" {
					t.Fatalf("тип ответа %q", resp.Header.Get("Content-Type"))
				}
				if len(*denied) != before+1 || (*denied)[before].op != op {
					t.Fatalf("отказ не зафиксирован: %+v", *denied)
				}
			})
		}
	}
}

func TestEveryRouteRequiresToken(t *testing.T) {
	srv, _ := server(t)
	for _, r := range routes {
		for _, token := range []string{"", "bad"} {
			if resp := do(t, srv, r, token); resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s %s с токеном %q: статус %d, ожидали 401", r.method, r.path, token, resp.StatusCode)
			}
		}
	}
}

// Матрица прав в обработчике совпадает с x-bs-operation контракта, и операции есть в policy.go.
func TestOperationsMatchOpenAPI(t *testing.T) {
	pathRe := regexp.MustCompile(`^  (/\S*):\s*$`)
	methodRe := regexp.MustCompile(`^    (get|post|put|patch|delete):\s*$`)
	opRe := regexp.MustCompile(`^      x-bs-operation:\s*(\S+)\s*$`)
	spec := map[string]auth.Operation{}
	var path, current string
	for _, line := range strings.Split(strings.ReplaceAll(string(apispec.Spec), "\r\n", "\n"), "\n") {
		if m := pathRe.FindStringSubmatch(line); m != nil {
			path, current = m[1], ""
			continue
		}
		if m := methodRe.FindStringSubmatch(line); m != nil && path != "" {
			current = strings.ToUpper(m[1]) + " " + path
			spec[current] = ""
			continue
		}
		if m := opRe.FindStringSubmatch(line); m != nil && current != "" {
			spec[current] = auth.Operation(m[1])
		}
		if strings.HasPrefix(line, "components:") {
			break
		}
	}
	if len(spec) != len(routes) {
		t.Fatalf("операций в контракте %d, маршрутов в тесте %d: %v", len(spec), len(routes), spec)
	}
	for key, op := range spec {
		if op == "" {
			t.Errorf("%s: нет x-bs-operation", key)
			continue
		}
		if operations[key] != op {
			t.Errorf("%s: в контракте %s, в обработчике %s", key, op, operations[key])
		}
		if len(auth.RolesFor(op)) == 0 {
			t.Errorf("%s: операции %s нет в матрице прав", key, op)
		}
	}
	for key := range operations {
		if _, ok := spec[key]; !ok {
			t.Errorf("маршрут %s есть в обработчике, но не в контракте", key)
		}
	}
}

func problemOf(t *testing.T, resp reply) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(resp.Body, &p); err != nil {
		t.Fatalf("тело ответа %q: %v", resp.Body, err)
	}
	return p
}

func upload(t *testing.T, srv *httptest.Server, token string, body *bytes.Buffer, ctype string) reply {
	t.Helper()
	return send(t, srv, http.MethodPost, "/files", token, body, ctype)
}

func TestUploadTooLargeRejectedBeforeReadingIntoService(t *testing.T) {
	srv, _ := server(t)
	body, ctype := multipartBody(t, "CLINIC_SHARE", "big.csv", bytes.Repeat([]byte("a"), MaxFileBytes+1))
	resp := upload(t, srv, string(auth.RoleAnalyst), body, ctype)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("статус %d, ожидали 413", resp.StatusCode)
	}
	if p := problemOf(t, resp); !strings.Contains(p["detail"].(string), "5 МиБ") {
		t.Fatalf("сообщение: %v", p)
	}
}

func TestUploadFormErrorsInRussian(t *testing.T) {
	srv, _ := server(t)
	cases := []struct {
		name, kind string
		content    []byte
		title      string
	}{
		{"нет файла", "CLINIC_SHARE", nil, "Нет файла"},
		{"нет вида", "", []byte("x"), "Не выбран вид файла"},
		{"неизвестный вид", "PATIENTS", []byte("x"), "Не выбран вид файла"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, ctype := multipartBody(t, c.kind, "f.csv", c.content)
			resp := upload(t, srv, string(auth.RoleAnalyst), body, ctype)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("статус %d", resp.StatusCode)
			}
			if p := problemOf(t, resp); p["title"] != c.title {
				t.Fatalf("заголовок ошибки: %v", p)
			}
		})
	}
	// Не multipart — ошибка разбора запроса тоже по-русски.
	resp := upload(t, srv, string(auth.RoleAnalyst), bytes.NewBufferString("{}"), "application/json")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("не multipart: статус %d", resp.StatusCode)
	}
	if p := problemOf(t, resp); !strings.Contains(p["detail"].(string), "multipart/form-data") {
		t.Fatalf("сообщение: %v", p)
	}
}
