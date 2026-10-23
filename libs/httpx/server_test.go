package httpx_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
)

// Serve на заранее открытом слушателе: вызывающий знает фактический адрес (порт :0) до старта сервера.
func TestServeOnListenerAndStopByContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	httpx.NewHealth().Register(mux)
	srv := httpx.NewServer("", mux, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz: %d", resp.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("остановка по контексту: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("сервер не остановился по отмене контекста")
	}
}
