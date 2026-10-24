package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticHandlerServesWithExplicitTypes(t *testing.T) {
	r := newTestRenderer(t)
	h := http.StripPrefix("/static/", r.StaticHandler())
	cases := map[string]string{
		"/static/css/app.css":        "text/css; charset=utf-8",
		"/static/js/app.js":          "text/javascript; charset=utf-8",
		"/static/vendor/htmx.min.js": "text/javascript; charset=utf-8",
		"/static/vendor/ext/sse.js":  "text/javascript; charset=utf-8",
		"/static/img/emblem.svg":     "image/svg+xml",
	}
	for path, ct := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?v=test", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: код %d", path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); got != ct {
			t.Errorf("%s: Content-Type %q, want %q", path, got, ct)
		}
		if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
			t.Errorf("%s: версия совпала — долгий кэш, got %q", path, got)
		}
	}
}

// Каталоги, чужие расширения и любые сегменты ".." — 404. "/static/img/../css/app.css" после очистки указывает на
// существующий файл, но отклоняется до очистки: обработчик не полагается на то, что ServeMux уже почистил путь.
func TestStaticHandlerNoListingNoTraversal(t *testing.T) {
	h := http.StripPrefix("/static/", newTestRenderer(t).StaticHandler())
	for _, path := range []string{"/static/", "/static/vendor/", "/static/vendor", "/static/../ui.go", "/static/img/../css/app.css", "/static/nope.js", "/static/vendor/README.md"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: код %d, ожидался 404", path, rec.Code)
		}
	}
}

func TestStaticHandlerShortCacheForStaleVersion(t *testing.T) {
	h := http.StripPrefix("/static/", newTestRenderer(t).StaticHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/css/app.css?v=old", nil))
	if got := rec.Header().Get("Cache-Control"); strings.Contains(got, "immutable") {
		t.Fatalf("чужая версия не кэшируется навсегда: %q", got)
	}
}

func TestAssetVersionDefaultsToContentHash(t *testing.T) {
	r, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.AssetVersion()) != 12 {
		t.Fatalf("версия по хешу содержимого, got %q", r.AssetVersion())
	}
	if got := r.Asset("css/app.css"); got != "/static/css/app.css?v="+r.AssetVersion() {
		t.Fatalf("got %q", got)
	}
}
