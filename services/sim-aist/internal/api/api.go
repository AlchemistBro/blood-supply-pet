// Package api — HTTP-интерфейс имитатора АИСТ-М (C01).
//
// Маршруты «как у АИСТ-М» (ИНТ-01: изменения, статус, суточный снимок; ИНТ-05: закрепление за пациентом)
// подчиняются управляемым сбоям из POST /dev/faults. Маршруты /dev/* и /healthz сбоям не подчиняются,
// иначе тест не смог бы снять сбой. JWT нет: имитатор не часть системы. Номер случая пациента (K1)
// принимается в закреплении, но не возвращается и не пишется в логи.
package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/correlation"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-aist/internal/state"
)

const (
	defaultChangesLimit = 500
	maxChangesLimit     = 5000
	maxStatusIDs        = 1000
	maxSeedUnits        = 200_000
	maxBodyBytes        = 1 << 20
	// HeaderIdempotencyKey — ключ идемпотентности закрепления (ИНТ-05).
	HeaderIdempotencyKey = "Idempotency-Key"
	// HeaderReplayed — признак того, что ответ взят из ранее принятого закрепления.
	HeaderReplayed = "Idempotent-Replayed"
)

// SeedDefaults — параметры набора из libs/testdata, если запрос /dev/seed их не задал.
type SeedDefaults struct {
	Seed           int64
	Units          int
	WithdrawnShare float64
}

// Options — необязательные зависимости.
type Options struct {
	Seed SeedDefaults
	// Sleep выполняет задержку slowMs; в тестах подменяется, чтобы не ждать.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Handler — обработчики имитатора.
type Handler struct {
	store *state.Store
	ref   *refdata.Set
	clk   clock.Clock
	log   *slog.Logger
	opts  Options
}

// New собирает обработчики.
func New(store *state.Store, ref *refdata.Set, clk clock.Clock, log *slog.Logger, opts Options) *Handler {
	if opts.Sleep == nil {
		opts.Sleep = sleep
	}
	return &Handler{store: store, ref: ref, clk: clk, log: log, opts: opts}
}

// sleep — задержка ответа по таймеру процесса. Это не чтение времени для бизнес-логики, а имитация
// медленной сети, поэтому Clock здесь не нужен; запрос, отменённый клиентом, задержку прерывает.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("задержка прервана: %w", ctx.Err())
	}
}

// Routes возвращает обработчик со всеми маршрутами и стандартными middleware.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	httpx.NewHealth().Register(mux)

	mux.Handle("GET /units/changes", h.faulty(h.changes))
	mux.Handle("GET /units/status", h.faulty(h.status))
	mux.Handle("GET /snapshot.csv", h.faulty(h.snapshot))
	mux.Handle("POST /allocations", h.faulty(h.allocate))

	mux.HandleFunc("POST /dev/units", h.createUnit)
	mux.HandleFunc("POST /dev/units/{id}/withdraw", h.withdraw)
	mux.HandleFunc("POST /dev/units/{id}/status", h.setStatus)
	mux.HandleFunc("POST /dev/faults", h.setFaults)
	mux.HandleFunc("POST /dev/seed", h.seed)
	mux.HandleFunc("GET /dev/allocations", h.allocations)

	return httpx.Chain(mux, httpx.Standard(h.log)...)
}

// faulty применяет управляемые сбои: сначала недоступность (503 с Retry-After), затем задержку.
func (h *Handler) faulty(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down, left := h.store.Unavailable(); down {
			w.Header().Set("Retry-After", strconv.FormatInt(int64(math.Ceil(left.Seconds())), 10))
			httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "АИСТ-М недоступна",
				"Имитатор АИСТ-М в режиме сбоя. Повторите запрос позже.")
			return
		}
		if d := h.store.Slow(); d > 0 {
			if err := h.opts.Sleep(r.Context(), d); err != nil {
				return // клиент ушёл, отвечать некому
			}
		}
		next(w, r)
	})
}

type changesResponse struct {
	Units          []state.Unit `json:"units"`
	LastVersion    int64        `json:"last_version"`
	CurrentVersion int64        `json:"current_version"`
	HasMore        bool         `json:"has_more"`
}

// changes — ИНТ-01: единицы с версией больше since по возрастанию версии.
// Клиент передаёт last_version следующим запросом как since; has_more — есть следующая страница.
func (h *Handler) changes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var since int64
	var err error
	if s := q.Get("since"); s != "" {
		since, err = strconv.ParseInt(s, 10, 64)
	}
	if err != nil || since < 0 {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректный параметр since",
			"since — неотрицательное целое: последняя полученная версия.")
		return
	}
	limit := defaultChangesLimit
	if s := q.Get("limit"); s != "" {
		limit, err = strconv.Atoi(s)
	}
	if err != nil || limit < 1 || limit > maxChangesLimit {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректный параметр limit",
			fmt.Sprintf("limit — целое от 1 до %d.", maxChangesLimit))
		return
	}
	units, more := h.store.Changes(since, limit)
	last := since
	if len(units) > 0 {
		last = units[len(units)-1].Version
	}
	httpx.WriteJSON(w, http.StatusOK, changesResponse{
		Units: units, LastVersion: last, CurrentVersion: h.store.Version(), HasMore: more,
	})
}

type unitStatus struct {
	AistUnitCode     string    `json:"aist_unit_code"`
	Status           string    `json:"status"`
	WithdrawalReason string    `json:"withdrawal_reason,omitempty"`
	ClinicCode       string    `json:"clinic_code"`
	Version          int64     `json:"version"`
	ChangedAt        time.Time `json:"changed_at"`
}

type statusResponse struct {
	Units     []unitStatus `json:"units"`
	NotFound  []string     `json:"not_found"`
	CheckedAt time.Time    `json:"checked_at"`
}

// status — ИНТ-01, точечный статус: ids через запятую или повтором параметра.
func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	var ids []string
	for _, v := range r.URL.Query()["ids"] {
		for _, id := range strings.Split(v, ",") {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 || len(ids) > maxStatusIDs {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректный параметр ids",
			fmt.Sprintf("Передайте от 1 до %d кодов единиц через запятую.", maxStatusIDs))
		return
	}
	found, missing := h.store.Statuses(ids)
	out := statusResponse{Units: make([]unitStatus, 0, len(found)), NotFound: missing, CheckedAt: h.clk.Now()}
	for _, u := range found {
		out.Units = append(out.Units, unitStatus{
			AistUnitCode: u.AistUnitCode, Status: u.Status, WithdrawalReason: u.WithdrawalReason,
			ClinicCode: u.ClinicCode, Version: u.Version, ChangedAt: u.ChangedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

var snapshotHeader = []string{
	"aist_unit_code", "clinic_code", "component_kind", "blood_group", "phenotype", "volume_ml",
	"expires_at", "storage_location", "status", "withdrawal_reason", "version", "changed_at",
}

// snapshot — ИНТ-01, суточный снимок CSV: все единицы, включая выбывшие, по коду.
func (h *Handler) snapshot(w http.ResponseWriter, r *http.Request) {
	units := h.store.Snapshot()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="snapshot-%s.csv"`, h.clk.Now().Format(time.DateOnly)))
	cw := csv.NewWriter(w)
	_ = cw.Write(snapshotHeader)
	for _, u := range units {
		_ = cw.Write([]string{
			u.AistUnitCode, u.ClinicCode, u.ComponentKind, u.BloodGroup, u.Phenotype,
			strconv.FormatInt(u.VolumeML, 10), u.ExpiresAt.Format(time.RFC3339), u.StorageLocation,
			u.Status, u.WithdrawalReason, strconv.FormatInt(u.Version, 10), u.ChangedAt.Format(time.RFC3339Nano),
		})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		h.log.ErrorContext(r.Context(), "запись снимка", "err", err)
	}
}

type allocationRequest struct {
	IdempotencyKey      string   `json:"idempotency_key"`
	PatientCaseNo       string   `json:"patient_case_no"`
	RecipientClinicCode string   `json:"recipient_clinic_code"`
	AistUnitCodes       []string `json:"aist_unit_codes"`
}

// allocate — ИНТ-05: закрепление единиц за пациентом. Ключ — заголовок Idempotency-Key или поле
// idempotency_key. Повтор с тем же ключом и содержимым возвращает тот же ответ (201) с заголовком
// Idempotent-Replayed и не создаёт второго закрепления.
func (h *Handler) allocate(w http.ResponseWriter, r *http.Request) {
	var req allocationRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	key := r.Header.Get(HeaderIdempotencyKey)
	switch {
	case key != "" && req.IdempotencyKey != "" && key != req.IdempotencyKey:
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Ключи идемпотентности расходятся",
			"Ключ в заголовке Idempotency-Key и в поле idempotency_key должен совпадать.")
		return
	case key == "":
		key = req.IdempotencyKey
	}
	a, created, err := h.store.Allocate(state.AllocationRequest{
		IdempotencyKey: key, PatientCaseNo: req.PatientCaseNo,
		RecipientClinicCode: req.RecipientClinicCode, AistUnitCodes: req.AistUnitCodes,
	})
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			httpx.WriteProblem(w, r, http.StatusUnprocessableEntity, "Единица не найдена", err.Error())
			return
		}
		h.writeError(w, r, err)
		return
	}
	// В логе только ключ и число единиц: номер случая пациента (K1) в логи не попадает.
	h.log.InfoContext(r.Context(), "закрепление за пациентом", "allocation_id", a.AllocationID,
		"idempotency_key", a.IdempotencyKey, "units", len(a.AistUnitCodes), "replayed", !created,
		"correlation_id", correlation.From(r.Context()).Correlation)
	if !created {
		w.Header().Set(HeaderReplayed, "true")
	}
	httpx.WriteJSON(w, http.StatusCreated, a)
}

func (h *Handler) allocations(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"allocations": h.store.Allocations()})
}

type createUnitRequest struct {
	AistUnitCode    string    `json:"aist_unit_code"`
	ClinicCode      string    `json:"clinic_code"`
	ComponentKind   string    `json:"component_kind"`
	BloodGroup      string    `json:"blood_group"`
	Phenotype       string    `json:"phenotype"`
	VolumeML        int64     `json:"volume_ml"`
	ExpiresAt       time.Time `json:"expires_at"`
	StorageLocation string    `json:"storage_location"`
	Status          string    `json:"status"`
}

func (h *Handler) createUnit(w http.ResponseWriter, r *http.Request) {
	var req createUnitRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	u, err := h.store.Create(state.NewUnit(req))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.log.InfoContext(r.Context(), "единица создана", "aist_unit_code", u.AistUnitCode,
		"status", u.Status, "version", u.Version)
	httpx.WriteJSON(w, http.StatusCreated, u)
}

func (h *Handler) withdraw(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &req, true) {
		return
	}
	if req.Reason == "" {
		req.Reason = state.ReasonIssued
	}
	u, err := h.store.Withdraw(r.PathValue("id"), req.Reason)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.log.InfoContext(r.Context(), "единица выбыла", "aist_unit_code", u.AistUnitCode,
		"reason", u.WithdrawalReason, "version", u.Version)
	httpx.WriteJSON(w, http.StatusOK, u)
}

func (h *Handler) setStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	u, err := h.store.SetStatus(r.PathValue("id"), req.Status)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.log.InfoContext(r.Context(), "статус единицы", "aist_unit_code", u.AistUnitCode,
		"status", u.Status, "version", u.Version)
	httpx.WriteJSON(w, http.StatusOK, u)
}

type faultsRequest struct {
	// UnavailableFor — длительность недоступности в формате Go: "30s", "2m". Пусто — снять сбой.
	UnavailableFor string `json:"unavailableFor"`
	SlowMs         int64  `json:"slowMs"`
}

type faultsResponse struct {
	UnavailableFor   string     `json:"unavailableFor,omitempty"`
	UnavailableUntil *time.Time `json:"unavailableUntil,omitempty"`
	SlowMs           int64      `json:"slowMs"`
}

func (h *Handler) setFaults(w http.ResponseWriter, r *http.Request) {
	var req faultsRequest
	if !decodeJSON(w, r, &req, true) {
		return
	}
	var down time.Duration
	if req.UnavailableFor != "" {
		d, err := time.ParseDuration(req.UnavailableFor)
		if err != nil || d < 0 {
			httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректный unavailableFor",
				`Укажите длительность, например "30s" или "2m".`)
			return
		}
		down = d
	}
	if req.SlowMs < 0 {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректный slowMs", "slowMs — неотрицательное целое.")
		return
	}
	h.store.SetFaults(state.Faults{UnavailableFor: down, Slow: time.Duration(req.SlowMs) * time.Millisecond})
	resp := faultsResponse{SlowMs: req.SlowMs}
	if down > 0 {
		until := h.clk.Now().Add(down)
		resp.UnavailableFor = down.String()
		resp.UnavailableUntil = &until
	}
	h.log.InfoContext(r.Context(), "сбои имитатора", "unavailable_for", down.String(), "slow_ms", req.SlowMs)
	httpx.WriteJSON(w, http.StatusOK, resp)
}

type seedRequest struct {
	Seed           *int64   `json:"seed"`
	Units          *int     `json:"units"`
	WithdrawnShare *float64 `json:"withdrawn_share"`
}

// seed загружает набор из libs/testdata. Повторная загрузка того же набора ничего не добавляет.
func (h *Handler) seed(w http.ResponseWriter, r *http.Request) {
	var req seedRequest
	if !decodeJSON(w, r, &req, true) {
		return
	}
	cfg := SeedConfig(h.opts.Seed, h.clk.Now())
	if req.Seed != nil {
		cfg.Seed = *req.Seed
	}
	if req.Units != nil {
		cfg.Units = *req.Units
	}
	if req.WithdrawnShare != nil {
		cfg.WithdrawnShare = *req.WithdrawnShare
	}
	if cfg.Units < 0 || cfg.Units > maxSeedUnits || cfg.WithdrawnShare < 0 || cfg.WithdrawnShare > 1 {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректные параметры набора",
			fmt.Sprintf("units — от 0 до %d, withdrawn_share — от 0 до 1.", maxSeedUnits))
		return
	}
	created, skipped, err := h.store.Seed(testdata.Generate(h.ref, cfg).Units)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.log.InfoContext(r.Context(), "набор загружен", "seed", cfg.Seed, "created", created, "skipped", skipped)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"created": created, "skipped": skipped, "version": h.store.Version(),
	})
}

// SeedConfig — параметры генератора: сроки годности отсчитываются от текущего времени.
func SeedConfig(d SeedDefaults, now time.Time) testdata.Config {
	return testdata.Config{Seed: d.Seed, Units: d.Units, Now: now, WithdrawnShare: d.WithdrawnShare}
}

// decodeJSON читает тело запроса. allowEmpty — пустое тело допустимо (все поля по умолчанию).
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, allowEmpty bool) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if allowEmpty && errors.Is(err, io.EOF) {
			return true
		}
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректное тело запроса",
			"Ожидается JSON с известными полями: "+err.Error())
		return false
	}
	return true
}

// writeError переводит ошибки состояния в коды ответа.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, state.ErrNotFound):
		httpx.WriteProblem(w, r, http.StatusNotFound, "Единица не найдена", err.Error())
	case errors.Is(err, state.ErrInvalid):
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректные данные", err.Error())
	case errors.Is(err, state.ErrIdempotencyMismatch):
		httpx.WriteProblem(w, r, http.StatusUnprocessableEntity, "Ключ уже использован",
			"Этот ключ идемпотентности уже принят с другим содержимым закрепления.")
	case errors.Is(err, state.ErrAlreadyExists), errors.Is(err, state.ErrWithdrawn),
		errors.Is(err, state.ErrQuarantine), errors.Is(err, state.ErrUnitUnavailable),
		errors.Is(err, state.ErrUnitTaken):
		httpx.WriteProblem(w, r, http.StatusConflict, "Конфликт состояния единицы", err.Error())
	default:
		h.log.ErrorContext(r.Context(), "ошибка имитатора", "err", err)
		httpx.WriteProblem(w, r, http.StatusInternalServerError, "Внутренняя ошибка", "Запрос не выполнен.")
	}
}
