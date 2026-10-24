package httpx_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
)

func TestRetryRestoresIdempotentRequestBody(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		if len(bodies) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, srv.URL, strings.NewReader(`{"value":42}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := httpx.NewClient(httpx.ClientConfig{Name: "retry-body", Retries: 1}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if len(bodies) != 2 || bodies[0] != bodies[1] || bodies[1] == "" {
		t.Fatalf("тела попыток: %v", bodies)
	}
}
