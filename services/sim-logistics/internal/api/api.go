// Package api — HTTP-интерфейс имитатора перевозчика C03. Имитатор не часть системы: без JWT и без OpenAPI
// в libs/contracts, им пользуется только logistics-gateway и тесты.
//
// API перевозчика (вызовы отправки, сверки и отмены подвержены сбою failNextCalls):
//
//	POST   /shipments                 создать заказ; 201 — заказ, 400 — некорректный, 422 — отказ перевозчика,
//	                                  503 — недоступен; при loseNextSubmitResponse соединение обрывается без ответа
//	GET    /shipments?clientRef=      активные (неотменённые) заказы по clientRef: {"shipments": [...]}
//	DELETE /shipments/{shipmentRef}   отменить один заказ по номеру перевозчика C03-n; 404, если номера нет
//	DELETE /shipments?clientRef=      отменить все заказы clientRef, как cancel прототипа; пустой список — не ошибка
//	GET    /estimates?sourceClinicId=&targetClinicId=   синтетические оценки времени доставки (демо)
//
// Пульт тестов и демо (сбоям не подвержен):
//
//	POST   /dev/faults     {"failNextCalls": n, "loseNextSubmitResponse": bool, "refuseNext": "причина"|true|false, "slowMs": n}
//	GET    /dev/shipments  все заказы, включая отменённые; ?clientRef= фильтрует
//	POST   /dev/reset      удалить заказы и сбои (нумерация C03-n продолжается)
//	GET    /healthz, /readyz
//
// Отмена по номеру и по clientRef разведена по форме запроса (путь или параметр), поэтому clientRef,
// похожий на номер C03-n, не может отменить чужой заказ.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-logistics/internal/carrier"
)

// Типы problem+json, по которым шлюз различает исходы отправки.
const (
	ProblemRefused     = "urn:bloodsupply:sim-logistics:carrier-refused"
	ProblemUnavailable = "urn:bloodsupply:sim-logistics:carrier-unavailable"
)

// defaultRefuseReason — причина отказа, если пульт прислал "refuseNext": true.
const defaultRefuseReason = "нет свободной машины с термоконтейнером (демо)"

// maxSlow — верхняя граница задержки: дольше упирается в таймаут записи HTTP-сервера.
const maxSlow = 30 * time.Second

const maxBodyBytes = 64 << 10

// Deps — зависимости обработчика.
type Deps struct {
	Carrier   *carrier.Carrier
	Estimator *carrier.Estimator
	Log       *slog.Logger
	// Metrics — обработчик /metrics; nil — маршрута нет.
	Metrics http.Handler
}

// Shipment — заказ в ответах API. Номеров единиц крови нет (ADR-0011).
type Shipment struct {
	ShipmentRef       string     `json:"shipmentRef"`
	ClientRef         string     `json:"clientRef"`
	SourceClinicID    int64      `json:"sourceClinicId"`
	TargetClinicID    int64      `json:"targetClinicId"`
	ComponentKind     string     `json:"componentKind"`
	Containers        int        `json:"containers"`
	TemperatureRegime string     `json:"temperatureRegime"`
	Cancelled         bool       `json:"cancelled"`
	CreatedAt         time.Time  `json:"createdAt"`
	CancelledAt       *time.Time `json:"cancelledAt,omitempty"`
}

// ShipmentList — список заказов.
type ShipmentList struct {
	Shipments []Shipment `json:"shipments"`
}

// FaultsState — текущие сбои.
type FaultsState struct {
	FailNextCalls          int    `json:"failNextCalls"`
	LoseNextSubmitResponse bool   `json:"loseNextSubmitResponse"`
	RefuseNext             string `json:"refuseNext,omitempty"`
	SlowMs                 int64  `json:"slowMs"`
}

// Estimate — оценка до одной клиники.
type Estimate struct {
	TargetClinicID int64 `json:"targetClinicId"`
	Minutes        int64 `json:"minutes"`
}

// EstimateList — оценки от клиники-источника. Demo всегда true: числа синтетические (ADR-0010).
type EstimateList struct {
	SourceClinicID int64      `json:"sourceClinicId"`
	EstimatedAt    time.Time  `json:"estimatedAt"`
	Demo           bool       `json:"demo"`
	Estimates      []Estimate `json:"estimates"`
}

type orderRequest struct {
	ClientRef         string `json:"clientRef"`
	SourceClinicID    int64  `json:"sourceClinicId"`
	TargetClinicID    int64  `json:"targetClinicId"`
	ComponentKind     string `json:"componentKind"`
	Containers        int    `json:"containers"`
	TemperatureRegime string `json:"temperatureRegime"`
}

type faultsRequest struct {
	FailNextCalls          *int            `json:"failNextCalls"`
	LoseNextSubmitResponse *bool           `json:"loseNextSubmitResponse"`
	RefuseNext             json.RawMessage `json:"refuseNext"`
	SlowMs                 *int64          `json:"slowMs"`
}

type handler struct {
	carrier   *carrier.Carrier
	estimator *carrier.Estimator
	log       *slog.Logger
}

// NewHandler собирает маршруты и стандартные middleware. Самый внешний слой запоминает исходный
// ResponseWriter: обёртка журнала доступа из libs/httpx не отдаёт http.Hijacker, а обрыв соединения
// для loseNextSubmitResponse делается через него.
func NewHandler(d Deps) http.Handler {
	h := &handler{carrier: d.Carrier, estimator: d.Estimator, log: d.Log}
	mux := http.NewServeMux()
	httpx.NewHealth().Register(mux)
	if d.Metrics != nil {
		mux.Handle("GET /metrics", d.Metrics)
	}
	mux.HandleFunc("POST /shipments", h.submit)
	mux.HandleFunc("GET /shipments", h.findActive)
	mux.HandleFunc("DELETE /shipments/{shipmentRef}", h.cancelByShipmentRef)
	mux.HandleFunc("DELETE /shipments", h.cancelByClientRef)
	mux.HandleFunc("GET /estimates", h.estimates)
	mux.HandleFunc("POST /dev/faults", h.setFaults)
	mux.HandleFunc("GET /dev/shipments", h.devShipments)
	mux.HandleFunc("POST /dev/reset", h.reset)
	mws := append([]httpx.Middleware{keepRawWriter()}, httpx.Standard(d.Log)...)
	return httpx.Chain(mux, mws...)
}

func (h *handler) submit(w http.ResponseWriter, r *http.Request) {
	var req orderRequest
	if err := decodeStrict(w, r, &req); err != nil {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректный заказ", err.Error())
		return
	}
	res, err := h.carrier.Submit(r.Context(), carrier.Order{
		ClientRef:         req.ClientRef,
		SourceClinicID:    req.SourceClinicID,
		TargetClinicID:    req.TargetClinicID,
		ComponentKind:     req.ComponentKind,
		Containers:        req.Containers,
		TemperatureRegime: carrier.TemperatureRegime(req.TemperatureRegime),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if res.ResponseLost {
		h.log.WarnContext(r.Context(), "заказ C03 создан, ответ нарочно потерян",
			"shipment_ref", res.Shipment.ShipmentRef, "client_ref", res.Shipment.ClientRef)
		h.dropConnection(w, r)
		return
	}
	h.log.InfoContext(r.Context(), "заказ C03 создан",
		"shipment_ref", res.Shipment.ShipmentRef, "client_ref", res.Shipment.ClientRef)
	httpx.WriteJSON(w, http.StatusCreated, toShipment(res.Shipment))
}

func (h *handler) findActive(w http.ResponseWriter, r *http.Request) {
	clientRef := r.URL.Query().Get("clientRef")
	if clientRef == "" {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Не указан clientRef", "Укажите параметр clientRef.")
		return
	}
	list, err := h.carrier.FindActive(clientRef)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toList(list))
}

func (h *handler) cancelByShipmentRef(w http.ResponseWriter, r *http.Request) {
	s, err := h.carrier.CancelByShipmentRef(r.PathValue("shipmentRef"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.log.InfoContext(r.Context(), "заказ C03 отменён", "shipment_ref", s.ShipmentRef, "client_ref", s.ClientRef)
	httpx.WriteJSON(w, http.StatusOK, toShipment(s))
}

func (h *handler) cancelByClientRef(w http.ResponseWriter, r *http.Request) {
	clientRef := r.URL.Query().Get("clientRef")
	if clientRef == "" {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Не указан заказ",
			"Укажите номер заказа в пути (/shipments/C03-n) или параметр clientRef.")
		return
	}
	list, err := h.carrier.CancelByClientRef(clientRef)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.log.InfoContext(r.Context(), "заказы C03 отменены по clientRef", "client_ref", clientRef, "count", len(list))
	httpx.WriteJSON(w, http.StatusOK, toList(list))
}

func (h *handler) estimates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	source, err := parseClinicID(q.Get("sourceClinicId"), true)
	if err != nil {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректная клиника-источник", err.Error())
		return
	}
	target, err := parseClinicID(q.Get("targetClinicId"), false)
	if err != nil {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректная клиника-получатель", err.Error())
		return
	}
	est, err := h.estimator.Estimate(source, target)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := EstimateList{SourceClinicID: est.SourceClinicID, EstimatedAt: est.EstimatedAt, Demo: true,
		Estimates: make([]Estimate, 0, len(est.Estimates))}
	for _, e := range est.Estimates {
		out.Estimates = append(out.Estimates, Estimate{TargetClinicID: e.TargetClinicID, Minutes: e.Minutes})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *handler) setFaults(w http.ResponseWriter, r *http.Request) {
	var req faultsRequest
	if err := decodeStrict(w, r, &req); err != nil {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректные сбои", err.Error())
		return
	}
	refuse, refuseSet, err := parseRefuse(req.RefuseNext)
	if err != nil {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректные сбои", err.Error())
		return
	}
	if req.FailNextCalls != nil && *req.FailNextCalls < 0 {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректные сбои", "failNextCalls не может быть отрицательным.")
		return
	}
	// Сравнение в миллисекундах: перевод огромного slowMs в Duration переполнился бы и прошёл проверку.
	if req.SlowMs != nil && (*req.SlowMs < 0 || *req.SlowMs > maxSlow.Milliseconds()) {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректные сбои",
			fmt.Sprintf("slowMs должен быть от 0 до %d.", maxSlow.Milliseconds()))
		return
	}
	f := h.carrier.UpdateFaults(func(f *carrier.Faults) {
		if req.FailNextCalls != nil {
			f.FailNextCalls = *req.FailNextCalls
		}
		if req.LoseNextSubmitResponse != nil {
			f.LoseNextSubmitResponse = *req.LoseNextSubmitResponse
		}
		if refuseSet {
			f.RefuseNext = refuse
		}
		if req.SlowMs != nil {
			f.Slow = time.Duration(*req.SlowMs) * time.Millisecond
		}
	})
	h.log.InfoContext(r.Context(), "сбои C03 изменены", "fail_next_calls", f.FailNextCalls,
		"lose_next_submit_response", f.LoseNextSubmitResponse, "refuse_next", f.RefuseNext != "",
		"slow_ms", f.Slow.Milliseconds())
	httpx.WriteJSON(w, http.StatusOK, FaultsState{
		FailNextCalls:          f.FailNextCalls,
		LoseNextSubmitResponse: f.LoseNextSubmitResponse,
		RefuseNext:             f.RefuseNext,
		SlowMs:                 f.Slow.Milliseconds(),
	})
}

func (h *handler) devShipments(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, toList(h.carrier.All(r.URL.Query().Get("clientRef"))))
}

func (h *handler) reset(w http.ResponseWriter, r *http.Request) {
	h.carrier.Reset()
	h.log.InfoContext(r.Context(), "имитатор C03 сброшен")
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var refused *carrier.RefusedError
	switch {
	case errors.As(err, &refused):
		h.log.InfoContext(r.Context(), "перевозчик отказал в заказе")
		httpx.WriteProblemFull(w, r, httpx.Problem{Type: ProblemRefused, Status: http.StatusUnprocessableEntity,
			Title: "Перевозчик отказал", Detail: refused.Reason})
	case errors.Is(err, carrier.ErrUnavailable):
		httpx.WriteProblemFull(w, r, httpx.Problem{Type: ProblemUnavailable, Status: http.StatusServiceUnavailable,
			Title: "Перевозчик недоступен", Detail: "C03 не отвечает. Повторите позже."})
	case errors.Is(err, carrier.ErrInvalid):
		httpx.WriteProblem(w, r, http.StatusBadRequest, "Некорректный запрос", err.Error())
	case errors.Is(err, carrier.ErrNotFound):
		httpx.WriteProblem(w, r, http.StatusNotFound, "Не найдено", err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// Клиент ушёл во время задержки: заказ не создан, ответ всё равно никто не прочтёт.
		h.log.InfoContext(r.Context(), "клиент ушёл до ответа C03", "err", err)
		httpx.WriteProblemFull(w, r, httpx.Problem{Type: ProblemUnavailable, Status: http.StatusServiceUnavailable,
			Title: "Запрос прерван", Detail: "Заказ не создан."})
	default:
		h.log.ErrorContext(r.Context(), "ошибка имитатора C03", "err", err)
		httpx.WriteProblem(w, r, http.StatusInternalServerError, "Внутренняя ошибка", "Повторите позже.")
	}
}

// dropConnection закрывает TCP-соединение без ответа: у клиента «таймаут чтения», исход заказа неизвестен.
func (h *handler) dropConnection(w http.ResponseWriter, r *http.Request) {
	raw := rawWriterFrom(r.Context())
	if raw == nil {
		raw = w
	}
	conn, _, err := http.NewResponseController(raw).Hijack()
	if err != nil {
		// Без Hijacker (HTTP/2, тестовый рекордер) ответ хотя бы не подтверждает заказ.
		h.log.ErrorContext(r.Context(), "обрыв соединения недоступен, отвечаю 504", "err", err)
		httpx.WriteProblemFull(w, r, httpx.Problem{Type: ProblemUnavailable, Status: http.StatusGatewayTimeout,
			Title: "Ответ потерян", Detail: "Исход заказа неизвестен: проверьте заказ по clientRef."})
		return
	}
	if err := conn.Close(); err != nil {
		h.log.WarnContext(r.Context(), "закрытие соединения", "err", err)
	}
}

func decodeStrict(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("разбор тела запроса: %w", err)
	}
	return nil
}

// parseRefuse принимает строку-причину, true (причина по умолчанию) или false (снять отказ).
func parseRefuse(raw json.RawMessage) (reason string, set bool, err error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false, nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		if b {
			return defaultRefuseReason, true, nil
		}
		return "", true, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false, errors.New("refuseNext: ожидается строка-причина или true/false")
	}
	return s, true, nil
}

func parseClinicID(s string, required bool) (int64, error) {
	if s == "" {
		if required {
			return 0, errors.New("параметр обязателен")
		}
		return 0, nil
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("ожидается положительное целое, получено %q", s)
	}
	return id, nil
}

func toShipment(s carrier.Shipment) Shipment {
	return Shipment{
		ShipmentRef:       s.ShipmentRef,
		ClientRef:         s.ClientRef,
		SourceClinicID:    s.SourceClinicID,
		TargetClinicID:    s.TargetClinicID,
		ComponentKind:     s.ComponentKind,
		Containers:        s.Containers,
		TemperatureRegime: string(s.TemperatureRegime),
		Cancelled:         s.Cancelled,
		CreatedAt:         s.CreatedAt,
		CancelledAt:       s.CancelledAt,
	}
}

func toList(list []carrier.Shipment) ShipmentList {
	out := ShipmentList{Shipments: make([]Shipment, 0, len(list))}
	for _, s := range list {
		out.Shipments = append(out.Shipments, toShipment(s))
	}
	return out
}

type rawWriterKey struct{}

// keepRawWriter кладёт в контекст исходный ResponseWriter сервера, пока его не обернули middleware.
func keepRawWriter() httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), rawWriterKey{}, w)))
		})
	}
}

func rawWriterFrom(ctx context.Context) http.ResponseWriter {
	w, _ := ctx.Value(rawWriterKey{}).(http.ResponseWriter)
	return w
}
