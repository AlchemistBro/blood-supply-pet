package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheck(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer ok.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()

	if err := check(ok.URL, time.Second); err != nil {
		t.Fatalf("здоровый сервис: %v", err)
	}
	if err := check(down.URL, time.Second); err == nil {
		t.Fatal("503 принят за здоровье")
	}
	if code := run(nil); code != 2 {
		t.Fatalf("без аргумента: код %d", code)
	}
}
