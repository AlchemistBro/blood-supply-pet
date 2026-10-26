package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestSeedPreparesNormsBeforeBulkSynchronization(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/dev/seed-norms":
			if len(calls) != 2 || calls[0] != "/clinics/1/sync" || r.Header.Get("Authorization") != "Bearer it-token" {
				t.Error("norms must precede the expensive inventory import and use IT role")
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			var body struct {
				Year int `json:"consumption_year"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Year != 2025 {
				t.Error("wrong consumption year")
			}
		case "/dev/seed":
			var body struct {
				Units int `json:"units"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Units != 10000 {
				t.Error("bulk inventory size changed")
			}
		case "/clinics/1/sync":
			if r.Header.Get("Authorization") != "Bearer operator-token" {
				t.Error("sync role changed")
			}
			_, _ = w.Write([]byte(`{"result":"SUCCESS","skipped":false}`))
		default:
			t.Error("unexpected seed operation")
		}
	}))
	defer server.Close()
	s := seeder{ctx: context.Background(), http: server.Client(), inventory: server.URL, aist: server.URL}
	if err := s.prepareInventory("it-token", "operator-token", 2026); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(calls, []string{"/clinics/1/sync", "/dev/seed-norms", "/dev/seed", "/clinics/1/sync"}) {
		t.Fatalf("seed order: %v", calls)
	}
}

type seedTransport func(*http.Request) (*http.Response, error)

func (f seedTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNormSeedResumesAfterTimeout(t *testing.T) {
	calls := 0
	s := seeder{ctx: context.Background(), inventory: "http://inventory.invalid", http: &http.Client{
		Transport: seedTransport(func(*http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return nil, context.DeadlineExceeded
			}
			return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(http.NoBody)}, nil
		}),
	}}
	if err := s.seedNorms("it", 2025); err != nil || calls != 2 {
		t.Fatalf("resume: calls=%d err=%v", calls, err)
	}
}

func TestNormSeedTimeoutRespectsParentDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	s := seeder{ctx: ctx, inventory: "http://inventory.invalid", http: &http.Client{
		Transport: seedTransport(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
	}}
	if err := s.seedNorms("it", 2025); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline lost: %v", err)
	}
}

func TestPartialSyncIsNotAccepted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":"PARTIAL","skipped":false}`))
	}))
	defer server.Close()
	s := seeder{ctx: context.Background(), inventory: server.URL, http: server.Client()}
	if err := s.waitSync("operator"); err == nil {
		t.Fatal("partial inventory accepted")
	}
}

func TestFailedNormPreparationDoesNotStartBulkImport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/clinics/1/sync" {
			_, _ = w.Write([]byte(`{"result":"SUCCESS","skipped":false}`))
			return
		}
		if r.URL.Path != "/dev/seed-norms" {
			t.Error("bulk import started despite failed norms")
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	s := seeder{ctx: context.Background(), http: server.Client(), inventory: server.URL, aist: server.URL}
	if err := s.prepareInventory("it", "operator", 2026); err == nil {
		t.Fatal("failed norms accepted")
	}
}
