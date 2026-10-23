package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-logistics/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/sim-logistics/internal/carrier"
)

var start = time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)

type fixture struct {
	srv    *httptest.Server
	client *http.Client
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clk := clock.NewMutable(start)
	h := api.NewHandler(api.Deps{
		Carrier:   carrier.New(clk),
		Estimator: carrier.NewEstimator([]carrier.Clinic{{ClinicID: 1, DistrictID: 1}, {ClinicID: 2, DistrictID: 3}}, clk),
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	// Без keep-alive: каждый запрос на своём соединении, обрыв одного не влияет на следующий,
	// а транспорт не повторяет POST на переиспользованном соединении.
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	return &fixture{srv: srv, client: client}
}

func orderBody(clientRef string) map[string]any {
	return map[string]any{
		"clientRef":         clientRef,
		"sourceClinicId":    1,
		"targetClinicId":    2,
		"componentKind":     "RBC",
		"containers":        2,
		"temperatureRegime": "COLD_2_6",
	}
}

func (f *fixture) do(t *testing.T, method, path string, body any) (int, []byte, error) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("кодирование тела: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rd)
	if err != nil {
		t.Fatalf("запрос: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

// mustDo выполняет запрос, проверяет статус и раскодирует ответ в out (если out не nil).
func (f *fixture) mustDo(t *testing.T, method, path string, body any, wantStatus int, out any) {
	t.Helper()
	status, data, err := f.do(t, method, path, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	if status != wantStatus {
		t.Fatalf("%s %s: статус %d, ожидали %d; тело: %s", method, path, status, wantStatus, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: разбор ответа %s: %v", method, path, data, err)
		}
	}
}

func (f *fixture) active(t *testing.T, clientRef string) []api.Shipment {
	t.Helper()
	var list api.ShipmentList
	f.mustDo(t, http.MethodGet, "/shipments?clientRef="+url.QueryEscape(clientRef), nil, http.StatusOK, &list)
	return list.Shipments
}

func (f *fixture) all(t *testing.T) []api.Shipment {
	t.Helper()
	var list api.ShipmentList
	f.mustDo(t, http.MethodGet, "/dev/shipments", nil, http.StatusOK, &list)
	return list.Shipments
}

func TestTwoIdenticalPostsCreateTwoShipments(t *testing.T) {
	f := newFixture(t)
	var first, second api.Shipment
	f.mustDo(t, http.MethodPost, "/shipments", orderBody("o07-a"), http.StatusCreated, &first)
	f.mustDo(t, http.MethodPost, "/shipments", orderBody("o07-a"), http.StatusCreated, &second)

	if first.ShipmentRef == second.ShipmentRef || !strings.HasPrefix(first.ShipmentRef, "C03-") {
		t.Fatalf("номера заказов: %q и %q", first.ShipmentRef, second.ShipmentRef)
	}
	if first.ClientRef != "o07-a" || first.TemperatureRegime != "COLD_2_6" || first.Containers != 2 ||
		first.SourceClinicID != 1 || first.TargetClinicID != 2 || first.ComponentKind != "RBC" || first.Cancelled {
		t.Fatalf("заказ: %+v", first)
	}
	if !first.CreatedAt.Equal(start) {
		t.Fatalf("время создания %v, ожидали время часов %v", first.CreatedAt, start)
	}
	if got := f.active(t, "o07-a"); len(got) != 2 {
		t.Fatalf("активных заказов %d, ожидали 2: имитатор нарочно не дедуплицирует", len(got))
	}
}

func TestLostResponseLeavesShipmentVisible(t *testing.T) {
	f := newFixture(t)
	f.mustDo(t, http.MethodPost, "/dev/faults", map[string]any{"loseNextSubmitResponse": true}, http.StatusOK, nil)

	status, data, err := f.do(t, http.MethodPost, "/shipments", orderBody("o07-b"))
	if err == nil {
		t.Fatalf("ответ должен потеряться (обрыв соединения), получили статус %d: %s", status, data)
	}

	got := f.active(t, "o07-b")
	if len(got) != 1 {
		t.Fatalf("после потерянного ответа заказов %d, ожидали 1: заказ создан, сверка должна его найти", len(got))
	}
	// Потеря одноразовая: следующий заказ получает ответ.
	var next api.Shipment
	f.mustDo(t, http.MethodPost, "/shipments", orderBody("o07-c"), http.StatusCreated, &next)
	if next.ShipmentRef == got[0].ShipmentRef {
		t.Fatalf("номер заказа повторился: %s", next.ShipmentRef)
	}
}

func TestFailNextCallsTwiceThenSuccess(t *testing.T) {
	f := newFixture(t)
	var faults api.FaultsState
	f.mustDo(t, http.MethodPost, "/dev/faults", map[string]any{"failNextCalls": 2}, http.StatusOK, &faults)
	if faults.FailNextCalls != 2 {
		t.Fatalf("состояние сбоев: %+v", faults)
	}

	f.mustDo(t, http.MethodPost, "/shipments", orderBody("o07-d"), http.StatusServiceUnavailable, nil)
	f.mustDo(t, http.MethodGet, "/shipments?clientRef=o07-d", nil, http.StatusServiceUnavailable, nil)
	if got := f.all(t); len(got) != 0 {
		t.Fatalf("недоступный перевозчик создал заказ: %+v", got)
	}
	f.mustDo(t, http.MethodPost, "/shipments", orderBody("o07-d"), http.StatusCreated, nil)
	if got := f.active(t, "o07-d"); len(got) != 1 {
		t.Fatalf("после двух сбоев заказов %d, ожидали 1", len(got))
	}
}

func TestFailNextCallsAffectsCancel(t *testing.T) {
	f := newFixture(t)
	var s api.Shipment
	f.mustDo(t, http.MethodPost, "/shipments", orderBody("o07-e"), http.StatusCreated, &s)
	f.mustDo(t, http.MethodPost, "/dev/faults", map[string]any{"failNextCalls": 1}, http.StatusOK, nil)
	f.mustDo(t, http.MethodDelete, "/shipments/"+s.ShipmentRef, nil, http.StatusServiceUnavailable, nil)
	if got := f.active(t, "o07-e"); len(got) != 1 {
		t.Fatalf("недоступный перевозчик отменил заказ: %+v", got)
	}
}

func TestRefuseNextRefusesWithoutShipment(t *testing.T) {
	f := newFixture(t)
	f.mustDo(t, http.MethodPost, "/dev/faults", map[string]any{"refuseNext": "нет свободной машины с термоконтейнером"}, http.StatusOK, nil)

	status, data, err := f.do(t, http.MethodPost, "/shipments", orderBody("o07-f"))
	if err != nil {
		t.Fatalf("отказ: %v", err)
	}
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("статус отказа %d, ожидали 422: %s", status, data)
	}
	var p struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("разбор отказа: %v", err)
	}
	if p.Type != api.ProblemRefused || p.Detail != "нет свободной машины с термоконтейнером" {
		t.Fatalf("отказ: %+v", p)
	}
	if got := f.all(t); len(got) != 0 {
		t.Fatalf("отказ создал заказ: %+v", got)
	}
	// Отказ одноразовый.
	f.mustDo(t, http.MethodPost, "/shipments", orderBody("o07-f"), http.StatusCreated, nil)
}

func TestRefuseNextAcceptsTrueWithDefaultReason(t *testing.T) {
	f := newFixture(t)
	var faults api.FaultsState
	f.mustDo(t, http.MethodPost, "/dev/faults", map[string]any{"refuseNext": true}, http.StatusOK, &faults)
	if faults.RefuseNext == "" {
		t.Fatalf("причина отказа по умолчанию не задана: %+v", faults)
	}
	f.mustDo(t, http.MethodPost, "/shipments", orderBody("o07-g"), http.StatusUnprocessableEntity, nil)
}

func TestCancelledShipmentHiddenFromActiveButVisibleInDev(t *testing.T) {
	f := newFixture(t)
	var byRef, byClient api.Shipment
	f.mustDo(t, http.MethodPost, "/shipments", orderBody("o07-h"), http.StatusCreated, &byRef)
	f.mustDo(t, http.MethodPost, "/shipments", orderBody("o07-i"), http.StatusCreated, &byClient)

	// Отмена по номеру заказа перевозчика.
	var cancelled api.Shipment
	f.mustDo(t, http.MethodDelete, "/shipments/"+byRef.ShipmentRef, nil, http.StatusOK, &cancelled)
	if !cancelled.Cancelled || cancelled.CancelledAt == nil {
		t.Fatalf("отменённый заказ: %+v", cancelled)
	}
	if got := f.active(t, "o07-h"); len(got) != 0 {
		t.Fatalf("отменённый заказ виден в сверке: %+v", got)
	}

	// Отмена по clientRef, как cancel прототипа.
	var list api.ShipmentList
	f.mustDo(t, http.MethodDelete, "/shipments?clientRef=o07-i", nil, http.StatusOK, &list)
	if len(list.Shipments) != 1 || !list.Shipments[0].Cancelled {
		t.Fatalf("отмена по clientRef: %+v", list)
	}
	if got := f.active(t, "o07-i"); len(got) != 0 {
		t.Fatalf("отменённый заказ виден в сверке: %+v", got)
	}

	all := f.all(t)
	if len(all) != 2 || !all[0].Cancelled || !all[1].Cancelled {
		t.Fatalf("/dev/shipments должен показывать отменённые заказы: %+v", all)
	}
	var filtered api.ShipmentList
	f.mustDo(t, http.MethodGet, "/dev/shipments?clientRef=o07-h", nil, http.StatusOK, &filtered)
	if len(filtered.Shipments) != 1 || filtered.Shipments[0].ShipmentRef != byRef.ShipmentRef {
		t.Fatalf("/dev/shipments?clientRef: %+v", filtered)
	}

	// Повторная отмена идемпотентна, неизвестный номер — 404, отмена без clientRef — 400.
	f.mustDo(t, http.MethodDelete, "/shipments/"+byRef.ShipmentRef, nil, http.StatusOK, nil)
	f.mustDo(t, http.MethodDelete, "/shipments/C03-999", nil, http.StatusNotFound, nil)
	f.mustDo(t, http.MethodDelete, "/shipments", nil, http.StatusBadRequest, nil)
	// Отмена по clientRef без заказов не ошибка.
	f.mustDo(t, http.MethodDelete, "/shipments?clientRef=o07-none", nil, http.StatusOK, nil)
}

func TestSubmitValidation(t *testing.T) {
	f := newFixture(t)
	withUnits := orderBody("o07-j")
	withUnits["unitIds"] = []string{"u-1"}
	badRegime := orderBody("o07-j")
	badRegime["temperatureRegime"] = "WARM"
	sameClinic := orderBody("o07-j")
	sameClinic["targetClinicId"] = 1
	noClientRef := orderBody("")

	for name, body := range map[string]map[string]any{
		"номера единиц в заказе":    withUnits,
		"неизвестный режим":         badRegime,
		"источник равен получателю": sameClinic,
		"пустой clientRef":          noClientRef,
	} {
		t.Run(name, func(t *testing.T) {
			f.mustDo(t, http.MethodPost, "/shipments", body, http.StatusBadRequest, nil)
		})
	}
	if got := f.all(t); len(got) != 0 {
		t.Fatalf("некорректный заказ создан: %+v", got)
	}
	f.mustDo(t, http.MethodGet, "/shipments", nil, http.StatusBadRequest, nil)
}

// Пульт принимает задержку и отвергает недопустимую. Само ожидание проверяет carrier
// (TestSlowSubmitHonoursContext, TestFindActiveAnswersDuringSlowSubmit) без реального времени.
func TestSlowMsIsSetAndValidated(t *testing.T) {
	f := newFixture(t)
	var faults api.FaultsState
	f.mustDo(t, http.MethodPost, "/dev/faults", map[string]any{"slowMs": 150}, http.StatusOK, &faults)
	if faults.SlowMs != 150 {
		t.Fatalf("состояние сбоев: %+v", faults)
	}
	f.mustDo(t, http.MethodPost, "/dev/faults", map[string]any{"slowMs": -1}, http.StatusBadRequest, nil)
	f.mustDo(t, http.MethodPost, "/dev/faults", map[string]any{"slowMs": 30_001}, http.StatusBadRequest, nil)
	// Перевод в Duration переполнился бы и дал отрицательную задержку, прошедшую проверку.
	f.mustDo(t, http.MethodPost, "/dev/faults", map[string]any{"slowMs": int64(math.MaxInt64)}, http.StatusBadRequest, nil)
	f.mustDo(t, http.MethodPost, "/dev/faults", map[string]any{"slowMs": 0}, http.StatusOK, &faults)
	if faults.SlowMs != 0 {
		t.Fatalf("задержка не снята: %+v", faults)
	}
}

func TestResetClearsShipmentsAndFaults(t *testing.T) {
	f := newFixture(t)
	f.mustDo(t, http.MethodPost, "/shipments", orderBody("o07-l"), http.StatusCreated, nil)
	f.mustDo(t, http.MethodPost, "/dev/faults", map[string]any{"failNextCalls": 5}, http.StatusOK, nil)
	f.mustDo(t, http.MethodPost, "/dev/reset", nil, http.StatusNoContent, nil)
	if got := f.all(t); len(got) != 0 {
		t.Fatalf("после сброса заказы: %+v", got)
	}
	f.mustDo(t, http.MethodPost, "/shipments", orderBody("o07-l"), http.StatusCreated, nil)
}

func TestEstimates(t *testing.T) {
	f := newFixture(t)
	var all, one, again api.EstimateList
	f.mustDo(t, http.MethodGet, "/estimates?sourceClinicId=1", nil, http.StatusOK, &all)
	if all.SourceClinicID != 1 || len(all.Estimates) != 1 || all.Estimates[0].TargetClinicID != 2 || !all.Demo {
		t.Fatalf("оценки: %+v", all)
	}
	if !all.EstimatedAt.Equal(start) {
		t.Fatalf("время оценки %v", all.EstimatedAt)
	}
	f.mustDo(t, http.MethodGet, "/estimates?sourceClinicId=1&targetClinicId=2", nil, http.StatusOK, &one)
	f.mustDo(t, http.MethodGet, "/estimates?sourceClinicId=2&targetClinicId=1", nil, http.StatusOK, &again)
	if one.Estimates[0].Minutes != all.Estimates[0].Minutes || again.Estimates[0].Minutes != one.Estimates[0].Minutes {
		t.Fatalf("оценка недетерминирована или несимметрична: %+v, %+v, %+v", all, one, again)
	}
	f.mustDo(t, http.MethodGet, "/estimates", nil, http.StatusBadRequest, nil)
	f.mustDo(t, http.MethodGet, "/estimates?sourceClinicId=abc", nil, http.StatusBadRequest, nil)
	f.mustDo(t, http.MethodGet, "/estimates?sourceClinicId=77", nil, http.StatusNotFound, nil)
}

func TestHealthz(t *testing.T) {
	f := newFixture(t)
	f.mustDo(t, http.MethodGet, "/healthz", nil, http.StatusOK, nil)
}
