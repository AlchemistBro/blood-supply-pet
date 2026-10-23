// Package api — пульт имитатора C02 «Моделирование ЧС». Имитатор не часть системы: без JWT на входе
// и без OpenAPI в libs/contracts. Прогноз синтетический (ADR-0010), сведений о пострадавших нет (NFR010).
//
//	POST /dev/forecast    сгенерировать прогноз и отправить в planning (ИНТ-02); параметры (query или форма):
//	                      scenario (ROAD_ACCIDENT | INDUSTRIAL_EXPLOSION | FLOOD), scale (0..100], seed,
//	                      receivers (true — с принимающими клиниками, false — распределит planning по долям ФП2),
//	                      inject (pii — подмешать запрещённые поля для теста белого списка), version (явная версия)
//	GET  /dev/forecasts   что отправлено, новые сверху (последние 100)
//	GET  /dev/scenarios   шаблоны сценариев
//	GET  /healthz, /readyz
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-emergency/internal/sender"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-emergency/internal/wire"
)

// historySize — сколько последних отправок помнить.
const historySize = 100

// Sender отправляет тело прогноза в planning.
type Sender interface {
	Send(ctx context.Context, body []byte) (sender.Result, error)
}

// Deps — зависимости пульта.
type Deps struct {
	Ref    *refdata.Set
	Clock  clock.Clock
	Sender Sender
	Log    *slog.Logger
	// DefaultSeed — seed, если запрос его не задал.
	DefaultSeed int64
	// Metrics — обработчик /metrics; nil — маршрута нет.
	Metrics http.Handler
}

// Record — одна отправка прогноза.
type Record struct {
	ScenarioVersion  string          `json:"scenario_version"`
	Scenario         string          `json:"scenario"`
	Seed             int64           `json:"seed"`
	Scale            float64         `json:"scale"`
	WithReceivers    bool            `json:"receivers"`
	Inject           string          `json:"inject,omitempty"`
	LinesCount       int             `json:"lines_count"`
	TotalVolumeML    int64           `json:"total_volume_ml"`
	SentAt           time.Time       `json:"sent_at"`
	PlanningStatus   int             `json:"planning_status,omitempty"`
	PlanningResponse string          `json:"planning_response,omitempty"`
	Error            string          `json:"error,omitempty"`
	Payload          json.RawMessage `json:"payload"`
	Demo             bool            `json:"demo"`
}

// RecordList — история отправок. Demo всегда true.
type RecordList struct {
	Demo      bool     `json:"demo"`
	Forecasts []Record `json:"forecasts"`
}

// Scenario — шаблон сценария для пульта.
type Scenario struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	Districts    int    `json:"districts"`
	BaseVolumeML int64  `json:"base_volume_ml"`
	HorizonHours int    `json:"horizon_hours"`
}

type handler struct {
	d       Deps
	mu      sync.Mutex
	history []Record
}

// NewHandler собирает маршруты и стандартные middleware (корреляция, восстановление, журнал доступа).
func NewHandler(d Deps) http.Handler {
	h := &handler{d: d}
	mux := http.NewServeMux()
	httpx.NewHealth().Register(mux)
	if d.Metrics != nil {
		mux.Handle("GET /metrics", d.Metrics)
	}
	mux.HandleFunc("POST /dev/forecast", h.sendForecast)
	mux.HandleFunc("GET /dev/forecasts", h.listForecasts)
	mux.HandleFunc("GET /dev/scenarios", h.listScenarios)
	return httpx.Chain(mux, httpx.Standard(d.Log)...)
}

type request struct {
	cfg    testdata.ForecastConfig
	inject wire.Inject
}

func (h *handler) parse(r *http.Request) (request, error) {
	cfg := testdata.ForecastConfig{
		Seed: h.d.DefaultSeed, Scenario: testdata.ScenarioRoadAccident, Scale: 1, WithReceivers: true,
		Now: h.d.Clock.Now(), Version: strings.TrimSpace(r.FormValue("version")),
	}
	if v := r.FormValue("scenario"); v != "" {
		cfg.Scenario = strings.ToUpper(strings.TrimSpace(v))
	}
	if v := r.FormValue("seed"); v != "" {
		seed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return request{}, errors.New("seed: ожидается целое число")
		}
		cfg.Seed = seed
	}
	if v := r.FormValue("scale"); v != "" {
		scale, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return request{}, errors.New("scale: ожидается число от 0 до 100")
		}
		cfg.Scale = scale
	}
	if v := r.FormValue("receivers"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return request{}, errors.New("receivers: ожидается true или false")
		}
		cfg.WithReceivers = b
	}
	inject, err := wire.ParseInject(r.FormValue("inject"))
	if err != nil {
		return request{}, err
	}
	return request{cfg: cfg, inject: inject}, nil
}

func (h *handler) sendForecast(w http.ResponseWriter, r *http.Request) {
	req, err := h.parse(r)
	if err != nil {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректные параметры прогноза", err.Error())
		return
	}
	f, err := testdata.GenerateForecast(h.d.Ref, req.cfg)
	if err != nil {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректные параметры прогноза", err.Error())
		return
	}
	body, err := wire.Body(f, req.inject)
	if err != nil {
		h.d.Log.ErrorContext(r.Context(), "кодирование прогноза", "err", err)
		httpx.WriteProblem(w, r, http.StatusInternalServerError, "Внутренняя ошибка", "Прогноз не сформирован.")
		return
	}
	rec := Record{
		ScenarioVersion: f.ScenarioVersion, Scenario: f.ScenarioKind, Seed: req.cfg.Seed, Scale: req.cfg.Scale,
		WithReceivers: req.cfg.WithReceivers, Inject: string(req.inject), LinesCount: len(f.Lines),
		SentAt: req.cfg.Now, Payload: body, Demo: true,
	}
	for _, l := range f.Lines {
		rec.TotalVolumeML += l.VolumeML
	}
	res, err := h.d.Sender.Send(r.Context(), body)
	rec.PlanningStatus, rec.PlanningResponse = res.Status, res.Body
	if err != nil {
		rec.Error = err.Error()
	}
	h.remember(rec)
	// Тело прогноза в лог не пишется: в режиме inject=pii в нём поля ПДн (пусть и ненастоящие).
	h.d.Log.InfoContext(r.Context(), "прогноз ЧС отправлен", "scenario_version", rec.ScenarioVersion,
		"scenario", rec.Scenario, "lines", rec.LinesCount, "inject", rec.Inject != "",
		"planning_status", rec.PlanningStatus, "failed", err != nil)
	switch {
	case errors.Is(err, sender.ErrNoServiceToken):
		httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "Нет служебного токена", err.Error())
	case err != nil:
		httpx.WriteProblem(w, r, http.StatusBadGateway, "planning не принял прогноз",
			"Прогноз не доставлен: "+err.Error()+". Запись есть в GET /dev/forecasts.")
	default:
		httpx.WriteJSON(w, http.StatusOK, rec)
	}
}

func (h *handler) remember(rec Record) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.history = append(h.history, rec)
	if len(h.history) > historySize {
		h.history = h.history[len(h.history)-historySize:]
	}
}

func (h *handler) listForecasts(w http.ResponseWriter, _ *http.Request) {
	h.mu.Lock()
	out := RecordList{Demo: true, Forecasts: make([]Record, 0, len(h.history))}
	for i := len(h.history) - 1; i >= 0; i-- {
		out.Forecasts = append(out.Forecasts, h.history[i])
	}
	h.mu.Unlock()
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *handler) listScenarios(w http.ResponseWriter, _ *http.Request) {
	list := testdata.ForecastScenarios()
	out := make([]Scenario, 0, len(list))
	for _, s := range list {
		out = append(out, Scenario{Code: s.Code, Name: s.Name, Districts: s.Districts, BaseVolumeML: s.BaseVolumeML, HorizonHours: s.HorizonHours})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
