//go:build e2e

// Сквозные сценарии саги подтверждения на стенде compose (этап 1, перенос scripts/smoke.sh прототипа):
// inventory, transfer, logistics-gateway и имитаторы АИСТ-М (C01) и перевозчика (C03).
//
// Подготовка каждого сценария: единица создаётся в sim-aist у клиники-источника C001, inventory подхватывает её
// опросом и публикует UnitRegistered, transfer строит копию; задание открывает служебный клиент, подтверждает
// оператор C001. Суррогатный unit_id наружу отдаётся только событием, поэтому тест берёт его из копии inventory
// (база на стенде), а готовность копии transfer — из его базы. Ожидания — опрос с таймаутом.
// Защищённый объём (NFR013) не задаётся: в source_stock он по умолчанию 0, а созданные тестом единицы входят
// в доступный остаток. Если на стенде вручную опубликовали большой защищённый объём для C001/RBC, его надо снять.
//
// Сценарии 4 и 5 останавливают и убивают контейнер logistics-gateway через docker compose — только контейнер
// стенда. Адреса и пути переопределяются переменными BS_E2E_*.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	transferURL     = env("BS_E2E_TRANSFER", "http://localhost:8105")
	gatewayURL      = env("BS_E2E_GATEWAY", "http://localhost:8106")
	simAistURL      = env("BS_E2E_SIM_AIST", "http://localhost:8201")
	simLogisticsURL = env("BS_E2E_SIM_LOGISTICS", "http://localhost:8203")
	// pgAdminURL — суперпользователь PostgreSQL стенда (порт POSTGRES_HOST_PORT); имя базы подставляется.
	pgAdminURL  = env("BS_E2E_PG", "postgres://postgres:postgres@localhost:5433")
	composeFile = env("BS_E2E_COMPOSE_FILE", filepath.Join("..", "..", "deploy", "compose", "compose.yaml"))
	composeEnv  = env("BS_E2E_COMPOSE_ENV", filepath.Join("..", "..", "deploy", "compose", ".env"))
)

const (
	sourceClinicID   = 1 // C001, оператор operator-c001
	sourceClinicCode = "C001"
	targetClinicID   = 2
	componentKind    = "RBC"
	// sagaTimeout — сколько ждать шага саги. Худший случай — сценарий 5: аренда убитого шлюза 30 с,
	// затем сверщик (период 1 с) и заказ.
	sagaTimeout = 2 * time.Minute
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

// --- HTTP ---

func doJSON(t *testing.T, method, rawURL, token string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, rawURL, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func mustJSON(t *testing.T, method, rawURL, token string, body any, want int, out any) {
	t.Helper()
	code, raw := doJSON(t, method, rawURL, token, body)
	if code != want {
		t.Fatalf("%s %s: %d %s, ожидали %d", method, rawURL, code, raw, want)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: разбор ответа: %v (%s)", method, rawURL, err, raw)
		}
	}
}

// eventually опрашивает cond до успеха или срока. cond возвращает описание текущего состояния для сообщения.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for {
		ok, state := cond()
		if ok {
			return
		}
		last = state
		if time.Now().After(deadline) {
			t.Fatalf("не дождались за %v: %s; последнее состояние: %s", timeout, what, last)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// --- базы стенда (только чтение, как оракул теста) ---

func dbQueryRow(t *testing.T, db, sql string, args []any, dest ...any) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, pgAdminURL+"/"+db+"?sslmode=disable")
	if err != nil {
		t.Fatalf("подключение к базе %s стенда: %v", db, err)
	}
	defer conn.Close(ctx)
	return conn.QueryRow(ctx, sql, args...).Scan(dest...)
}

// --- имитаторы ---

type simUnit struct {
	AistUnitCode string `json:"aist_unit_code"`
}

type shipment struct {
	ShipmentRef string `json:"shipmentRef"`
	ClientRef   string `json:"clientRef"`
	Cancelled   bool   `json:"cancelled"`
}

func clientRef(obligationID string) string { return "o07-" + strings.ToLower(obligationID) }

// shipments — все заказы у C03 по обязательству, включая отменённые.
func shipments(t *testing.T, obligationID string) []shipment {
	t.Helper()
	var list struct {
		Shipments []shipment `json:"shipments"`
	}
	mustJSON(t, http.MethodGet, simLogisticsURL+"/dev/shipments?clientRef="+url.QueryEscape(clientRef(obligationID)),
		"", nil, http.StatusOK, &list)
	return list.Shipments
}

// c03Faults задаёт сбои имитатора перевозчика и снимает их после теста.
func c03Faults(t *testing.T, faults map[string]any) {
	t.Helper()
	mustJSON(t, http.MethodPost, simLogisticsURL+"/dev/faults", "", faults, http.StatusOK, nil)
	t.Cleanup(func() {
		_, _ = doJSON(t, http.MethodPost, simLogisticsURL+"/dev/faults", "",
			map[string]any{"failNextCalls": 0, "loseNextSubmitResponse": false, "refuseNext": false, "slowMs": 0})
	})
}

// --- подготовка единицы и задания ---

type unit struct {
	AistCode string
	UnitID   string
}

// newUnit создаёт единицу в АИСТ-М и ждёт, пока её увидят inventory и transfer.
func newUnit(t *testing.T) unit {
	t.Helper()
	code := "E2E-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	var created simUnit
	mustJSON(t, http.MethodPost, simAistURL+"/dev/units", "", map[string]any{
		"aist_unit_code": code, "clinic_code": sourceClinicCode, "component_kind": componentKind,
		"blood_group": "O+", "storage_location": "e2e",
	}, http.StatusCreated, &created)

	var unitID string
	eventually(t, sagaTimeout, "единица "+code+" в копии inventory", func() (bool, string) {
		err := dbQueryRow(t, "inventory", `SELECT unit_id::text FROM unit WHERE aist_unit_code = $1`, []any{code}, &unitID)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, "нет строки"
		}
		if err != nil {
			t.Fatalf("копия inventory: %v", err)
		}
		return true, ""
	})
	eventually(t, sagaTimeout, "единица "+unitID+" в копии transfer", func() (bool, string) {
		var status string
		err := dbQueryRow(t, "transfer", `SELECT unit_status_code FROM unit_copy WHERE unit_id = $1`, []any{unitID}, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, "нет строки"
		}
		if err != nil {
			t.Fatalf("копия transfer: %v", err)
		}
		return status == "IN_STOCK", status
	})
	return unit{AistCode: code, UnitID: unitID}
}

type obligation struct {
	ObligationID string   `json:"obligation_id"`
	Status       string   `json:"status"`
	TransportRef string   `json:"transport_ref"`
	Units        []string `json:"units"`
}

// openObligation открывает задание источнику C001 служебным токеном (как matching после выбора врача).
func openObligation(t *testing.T, qty int) obligation {
	t.Helper()
	var o obligation
	mustJSON(t, http.MethodPost, transferURL+"/obligations", serviceToken(t), map[string]any{
		"choice_id": uuid.NewString(), "request_id": uuid.NewString(), "origin": "DOCTOR_CHOICE",
		"source_clinic_id": sourceClinicID, "target_clinic_id": targetClinicID, "component_kind": componentKind,
		"requested_qty": qty, "urgency": "PLANNED", "confirm_window_seconds": 3600,
		"spec": map[string]any{"rule_version": "e2e-demo"},
	}, http.StatusOK, &o)
	if o.Status != "PENDING" {
		t.Fatalf("открытое задание в статусе %s", o.Status)
	}
	return o
}

// confirm подтверждает задание оператором C001; ответ в том же запросе.
func confirm(t *testing.T, obligationID string, units ...unit) {
	t.Helper()
	ids := make([]string, 0, len(units))
	for _, u := range units {
		ids = append(ids, u.UnitID)
	}
	var res struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	code, raw := doJSON(t, http.MethodPost, transferURL+"/obligations/"+obligationID+"/confirm", login(t, "operator-c001"),
		map[string]any{"unit_ids": ids, "idempotency_key": uuid.NewString()})
	if err := json.Unmarshal(raw, &res); err != nil || code != http.StatusOK || res.Code != "CONFIRMED" {
		t.Fatalf("подтверждение %s: %d %s", obligationID, code, raw)
	}
}

func getObligation(t *testing.T, token, id string) obligation {
	t.Helper()
	var o obligation
	mustJSON(t, http.MethodGet, transferURL+"/obligations/"+id, token, nil, http.StatusOK, &o)
	return o
}

// awaitStatus опрашивает задание одним служебным токеном (живёт дольше срока ожидания).
func awaitStatus(t *testing.T, id, status string) obligation {
	t.Helper()
	token := serviceToken(t)
	var o obligation
	eventually(t, sagaTimeout, "задание "+id+" в статусе "+status, func() (bool, string) {
		o = getObligation(t, token, id)
		return o.Status == status, o.Status
	})
	return o
}

// --- docker compose (только контейнеры стенда) ---

func compose(t *testing.T, args ...string) {
	t.Helper()
	full := []string{"compose", "-f", composeFile}
	if _, err := os.Stat(composeEnv); err == nil {
		full = append(full, "--env-file", composeEnv)
	}
	full = append(full, "--profile", "services")
	full = append(full, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(full, " "), err, out)
	}
}

// startGateway поднимает шлюз и ждёт его /healthz. Регистрируется и как очистка: упавший тест не оставит
// стенд без шлюза.
func startGateway(t *testing.T) {
	t.Helper()
	compose(t, "start", "logistics-gateway")
	eventually(t, sagaTimeout, "logistics-gateway /healthz", func() (bool, string) {
		code, err := statusOf(gatewayURL + "/healthz")
		return code == http.StatusOK, fmt.Sprint(code, err)
	})
}

// statusOf — код ответа GET без разбора тела; ошибка сети — код 0 (контейнер ещё не слушает).
func statusOf(rawURL string) (int, error) {
	resp, err := httpClient.Get(rawURL)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// --- сценарии ---

// 1. Подтверждение двух единиц → TRANSPORT_REQUESTED и ровно один заказ у C03 с тем же номером.
func TestSagaConfirmationOrdersTransport(t *testing.T) {
	u1, u2 := newUnit(t), newUnit(t)
	o := openObligation(t, 2)
	confirm(t, o.ObligationID, u1, u2)

	done := awaitStatus(t, o.ObligationID, "TRANSPORT_REQUESTED")
	list := shipments(t, o.ObligationID)
	if len(list) != 1 || list[0].Cancelled || list[0].ShipmentRef != done.TransportRef {
		t.Fatalf("заказы у C03 %+v, transport_ref %q: ожидали один активный с этим номером", list, done.TransportRef)
	}
}

// 2. Закреплённая единица выдана в АИСТ-М → O07 BROKEN → CancelTransport → заказ у C03 снят.
func TestSagaWithdrawalBreaksObligationAndCancelsShipment(t *testing.T) {
	u1, u2 := newUnit(t), newUnit(t)
	o := openObligation(t, 2)
	confirm(t, o.ObligationID, u1, u2)
	awaitStatus(t, o.ObligationID, "TRANSPORT_REQUESTED")

	mustJSON(t, http.MethodPost, simAistURL+"/dev/units/"+url.PathEscape(u1.AistCode)+"/withdraw", "",
		map[string]any{"reason": "ISSUED"}, http.StatusOK, nil)

	awaitStatus(t, o.ObligationID, "BROKEN")
	eventually(t, sagaTimeout, "заказ у C03 снят", func() (bool, string) {
		list := shipments(t, o.ObligationID)
		return len(list) == 1 && list[0].Cancelled, fmt.Sprintf("%+v", list)
	})
}

// 3. C03 создаёт заказ, но ответ теряется → сверка находит его, второго заказа нет.
func TestSagaLostCarrierResponseGivesExactlyOneShipment(t *testing.T) {
	u := newUnit(t)
	o := openObligation(t, 1)
	c03Faults(t, map[string]any{"loseNextSubmitResponse": true})
	confirm(t, o.ObligationID, u)

	done := awaitStatus(t, o.ObligationID, "TRANSPORT_REQUESTED")
	list := shipments(t, o.ObligationID)
	if len(list) != 1 || list[0].ShipmentRef != done.TransportRef {
		t.Fatalf("у C03 %d заказов (%+v), ожидали ровно один с номером %q", len(list), list, done.TransportRef)
	}
}

// 4. Шлюз остановлен: подтверждение всё равно проходит (оператор не ждёт перевозчика), команда ждёт в Kafka,
// после старта шлюза заказ уходит.
func TestSagaConfirmationWhileGatewayIsDownOrdersAfterStart(t *testing.T) {
	u := newUnit(t)
	o := openObligation(t, 1)
	compose(t, "stop", "logistics-gateway")
	t.Cleanup(func() { compose(t, "start", "logistics-gateway") })

	confirm(t, o.ObligationID, u)
	if s := getObligation(t, serviceToken(t), o.ObligationID).Status; s != "CONFIRMED" {
		t.Fatalf("при остановленном шлюзе статус %s, ожидали CONFIRMED", s)
	}
	if list := shipments(t, o.ObligationID); len(list) != 0 {
		t.Fatalf("заказ у C03 без шлюза: %+v", list)
	}

	startGateway(t)
	done := awaitStatus(t, o.ObligationID, "TRANSPORT_REQUESTED")
	if list := shipments(t, o.ObligationID); len(list) != 1 || list[0].ShipmentRef != done.TransportRef {
		t.Fatalf("после старта шлюза заказы у C03 %+v", list)
	}
}

// 5. kill -9 шлюза посреди заказа (строка REQUESTING под арендой, отправка в C03 идёт) → после старта
// у C03 ровно один заказ: аренда упавшего истекает, сверщик сверяется и заказывает не больше одного раза.
func TestSagaGatewayKilledMidOrderGivesExactlyOneShipment(t *testing.T) {
	u := newUnit(t)
	o := openObligation(t, 1)
	// Медленная C03 держит отправку открытой: окно, в котором шлюз убивается посреди заказа.
	c03Faults(t, map[string]any{"slowMs": 10000})
	t.Cleanup(func() { compose(t, "start", "logistics-gateway") })
	confirm(t, o.ObligationID, u)

	eventually(t, sagaTimeout, "шлюз взял аренду заказа", func() (bool, string) {
		var status string
		var leased bool
		err := dbQueryRow(t, "logistics",
			`SELECT status, lease_owner IS NOT NULL FROM transport_request WHERE obligation_id = $1`,
			[]any{o.ObligationID}, &status, &leased)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, "нет строки"
		}
		if err != nil {
			t.Fatalf("база logistics: %v", err)
		}
		return status == "REQUESTING" && leased, fmt.Sprintf("%s, аренда %v", status, leased)
	})
	compose(t, "kill", "-s", "KILL", "logistics-gateway")
	mustJSON(t, http.MethodPost, simLogisticsURL+"/dev/faults", "", map[string]any{"slowMs": 0}, http.StatusOK, nil)

	startGateway(t)
	done := awaitStatus(t, o.ObligationID, "TRANSPORT_REQUESTED")
	list := shipments(t, o.ObligationID)
	if len(list) != 1 || list[0].ShipmentRef != done.TransportRef {
		t.Fatalf("после kill -9 шлюза у C03 %d заказов (%+v), ожидали ровно один с номером %q",
			len(list), list, done.TransportRef)
	}
}
