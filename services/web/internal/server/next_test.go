package server

import (
	"net/http"
	"strings"
	"testing"
)

// Открытое перенаправление после входа: всё, что браузер может прочитать как чужой хост, отклоняется.
func TestCleanLocalPathRejectsOpenRedirects(t *testing.T) {
	for _, next := range []string{
		"",
		"//evil.example/x",
		"https://evil.example/",
		"http:/evil.example",
		"/\\evil.example",
		"\\\\evil.example",
		"/x/../\\evil.example",    // path.Clean в http.Redirect дал бы /\evil.example
		"/x/..\\..\\evil.example", // обратная черта в любом месте
		"/%5C",                    // раскодированная обратная черта
		"/%5Cevil.example",
		"/..%2F%5Cevil.example", // /../\evil.example после раскодирования
		"/x/..%2F%5Cevil.example",
		"/%2F%2Fevil.example",    // //evil.example после раскодирования
		"/\t/evil.example",       // управляющий символ
		"/%09/evil.example",      // он же закодированный
		"/\r\nLocation: //evil",  // перевод строки
		"/ evil",                 // пробел
		"/∕evil.example",         // U+2215 DIVISION SLASH
		"/／evil.example",         // U+FF0F FULLWIDTH SOLIDUS
		"/%E2%88%95evil.example", // U+2215 закодированный
		"/%EF%BC%8Fevil.example", // U+FF0F закодированный
		"/tasks#frag",
		"/" + strings.Repeat("a", maxNextLen),
	} {
		if got, ok := cleanLocalPath(next); ok {
			t.Errorf("%q должен отклоняться, получено %q", next, got)
		}
	}
}

// Что бы ни пропустила проверка, результат — путь этого же сайта: один `/` в начале, без `\`.
func TestCleanLocalPathResultIsAlwaysLocal(t *testing.T) {
	for _, next := range []string{"/%2F%2Fevil.example/../..", "/a/../../..//evil", "/./%2e%2e/evil", "/tasks/..//x"} {
		got, ok := cleanLocalPath(next)
		if ok && (!strings.HasPrefix(got, "/") || strings.HasPrefix(got, "//") || strings.Contains(got, "\\")) {
			t.Errorf("%q пропущен как нелокальный %q", next, got)
		}
	}
}

func TestCleanLocalPathKeepsLocalPaths(t *testing.T) {
	for next, want := range map[string]string{
		"/":                     "/",
		"/tasks":                "/tasks",
		"/tasks?status=PENDING": "/tasks?status=PENDING",
		"/tasks/":               "/tasks",
		"/x/../tasks":           "/tasks",
		"/tasks/7?a=1&b=2":      "/tasks/7?a=1&b=2",
	} {
		got, ok := cleanLocalPath(next)
		if !ok || got != want {
			t.Errorf("%q: получено %q (%v), ожидалось %q", next, got, ok, want)
		}
	}
}

// Белый список — зарегистрированные маршруты web: неизвестный путь ведёт на главную.
func TestSafeNextOnlyRegisteredRoutes(t *testing.T) {
	s := &Server{mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /{$}", func(http.ResponseWriter, *http.Request) {})
	s.mux.HandleFunc("GET /tasks", func(http.ResponseWriter, *http.Request) {})
	for next, want := range map[string]string{
		"/tasks?status=PENDING":      "/tasks?status=PENDING",
		"/unknown":                   "/",
		"/x/../\\evil.example":       "/",
		"/%2F%2Fevil.example":        "/",
		"/tasks/../../evil.example/": "/",
	} {
		if got := s.safeNext(next); got != want {
			t.Errorf("%q: получено %q, ожидалось %q", next, got, want)
		}
	}
}
