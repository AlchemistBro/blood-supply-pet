// Package fakeaist — фальшивая АИСТ-М для тестов inventory: тот же HTTP-контракт, что у services/sim-aist
// (GET /units/changes, GET /units/status), но с управлением из теста. Пакеты имитатора лежат в его internal
// и недоступны inventory, поэтому здесь своя минимальная копия поведения.
package fakeaist

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Unit — единица в формате АИСТ-М.
type Unit struct {
	AistUnitCode     string    `json:"aist_unit_code"`
	ClinicCode       string    `json:"clinic_code"`
	ComponentKind    string    `json:"component_kind"`
	BloodGroup       string    `json:"blood_group"`
	Phenotype        string    `json:"phenotype,omitempty"`
	VolumeML         int64     `json:"volume_ml"`
	ExpiresAt        time.Time `json:"expires_at"`
	StorageLocation  string    `json:"storage_location"`
	Status           string    `json:"status"`
	WithdrawalReason string    `json:"withdrawal_reason,omitempty"`
	Version          int64     `json:"version"`
	ChangedAt        time.Time `json:"changed_at"`
}

// Server — фальшивая АИСТ-М на httptest.
type Server struct {
	URL string

	mu          sync.Mutex
	now         time.Time
	version     int64
	units       map[string]*Unit
	down        bool
	statusCalls int
	srv         *httptest.Server
}

// New поднимает сервер; закрывается в t.Cleanup.
func New(t testing.TB, now time.Time) *Server {
	t.Helper()
	s := &Server{now: now.UTC(), units: map[string]*Unit{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /units/changes", s.changes)
	mux.HandleFunc("GET /units/status", s.status)
	s.srv = httptest.NewServer(mux)
	s.URL = s.srv.URL
	t.Cleanup(s.srv.Close)
	return s
}

// Add создаёт единицу со следующей версией. Пустые поля получают значения по умолчанию.
func (s *Server) Add(u Unit) Unit {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u.ClinicCode == "" {
		u.ClinicCode = "C001"
	}
	if u.ComponentKind == "" {
		u.ComponentKind = "RBC"
	}
	if u.BloodGroup == "" {
		u.BloodGroup = "A+"
	}
	if u.VolumeML == 0 {
		u.VolumeML = 300
	}
	if u.ExpiresAt.IsZero() {
		u.ExpiresAt = s.now.Add(30 * 24 * time.Hour)
	}
	if u.Status == "" {
		u.Status = "IN_STOCK"
	}
	if u.StorageLocation == "" {
		u.StorageLocation = "холодильник 1"
	}
	c := u
	s.bump(&c)
	s.units[c.AistUnitCode] = &c
	return c
}

// Withdraw переводит единицу в выбывший статус по причине (EXPIRED приходит как DISCARDED).
func (s *Server) Withdraw(code, reason string) Unit {
	status := map[string]string{"ISSUED": "ISSUED", "DISCARDED": "DISCARDED", "TRANSFERRED": "TRANSFERRED", "EXPIRED": "DISCARDED"}[reason]
	return s.change(code, func(u *Unit) { u.Status, u.WithdrawalReason = status, reason })
}

// SetStatus меняет статус без причины (карантин и возврат из него).
func (s *Server) SetStatus(code, status string) Unit {
	return s.change(code, func(u *Unit) { u.Status, u.WithdrawalReason = status, "" })
}

func (s *Server) change(code string, fn func(*Unit)) Unit {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.units[code]
	fn(u)
	s.bump(u)
	return *u
}

func (s *Server) bump(u *Unit) {
	s.version++
	s.now = s.now.Add(time.Second)
	u.Version = s.version
	u.ChangedAt = s.now
}

// Restart имитирует перезапуск АИСТ-М без базы: состояние пропадает, версии снова с 1.
func (s *Server) Restart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.units = map[string]*Unit{}
	s.version = 0
}

// SetDown включает и выключает недоступность (503 на всех маршрутах).
func (s *Server) SetDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

// StatusCalls — сколько раз спрашивали точечный статус.
func (s *Server) StatusCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusCalls
}

func (s *Server) unavailable(w http.ResponseWriter) bool {
	if !s.down {
		return false
	}
	w.Header().Set("Retry-After", "30")
	http.Error(w, `{"title":"АИСТ-М недоступна","status":503}`, http.StatusServiceUnavailable)
	return true
}

func (s *Server) changes(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable(w) {
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 {
		limit = 500
	}
	var out []Unit
	for _, u := range s.units {
		if u.Version > since {
			out = append(out, *u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	last := since
	if len(out) > 0 {
		last = out[len(out)-1].Version
	}
	if out == nil {
		out = []Unit{}
	}
	writeJSON(w, map[string]any{"units": out, "last_version": last, "current_version": s.version, "has_more": more})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusCalls++
	if s.unavailable(w) {
		return
	}
	type status struct {
		AistUnitCode     string    `json:"aist_unit_code"`
		Status           string    `json:"status"`
		WithdrawalReason string    `json:"withdrawal_reason,omitempty"`
		ClinicCode       string    `json:"clinic_code"`
		Version          int64     `json:"version"`
		ChangedAt        time.Time `json:"changed_at"`
	}
	units := []status{}
	missing := []string{}
	for _, v := range r.URL.Query()["ids"] {
		for _, id := range strings.Split(v, ",") {
			u, ok := s.units[id]
			if !ok {
				missing = append(missing, id)
				continue
			}
			units = append(units, status{u.AistUnitCode, u.Status, u.WithdrawalReason, u.ClinicCode, u.Version, u.ChangedAt})
		}
	}
	writeJSON(w, map[string]any{"units": units, "not_found": missing, "checked_at": s.now})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
