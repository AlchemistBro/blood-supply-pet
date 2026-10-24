//go:build integration

package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/demand"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth/authtest"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testinfra"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/migrations"
)

func TestPlanRepeatDoesNotPublishCommandsTwice(t *testing.T) {
	ctx := context.Background()
	pool, err := bspgx.Connect(ctx, testinfra.Postgres(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := bspgx.Migrate(ctx, pool, messaging.Migrations, refdata.Migrations, migrations.FS); err != nil {
		t.Fatal(err)
	}
	ref := refdata.MustLoad()
	if err := bspgx.WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error { return ref.Seed(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	clk := clock.NewMutable(now)
	svc := app.New(pool, postgres.New(pool), messaging.NewJournal("planning", "planning", clk), ref, clk)
	send := func(payload contracts.Payload) {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Handle(ctx, messaging.Message{ID: uuid.New(), Type: payload.MessageType(), SchemaVersion: 1, OccurredAt: now, Payload: raw}); err != nil {
			t.Fatal(err)
		}
	}
	for range 5 {
		send(inventory.UnitRegistered{UnitID: uuid.New(), ClinicID: 1, ComponentKind: "RBC", BloodGroup: "O+", VolumeML: 300, Status: "IN_STOCK", Version: 1, ExpiresAt: now.Add(40 * 24 * time.Hour)})
	}
	send(inventory.StockNormPublished{ClinicID: 1, ComponentKind: "RBC", NormML: 300, AvgMonthlyML: 9000, Version: 1, CalculatedAt: now})
	send(inventory.StockSnapshotPublished{ClinicID: 1, ComponentKind: "RBC", BloodGroup: "O+", UnitCount: 5, VolumeML: 1500, Watermark: 1, SyncedAt: now})
	send(demand.DemandEstimatePublished{DemandEstimateID: uuid.New(), VersionNo: 1, ClinicShareVersionID: uuid.New(), PeriodStart: now, PeriodEnd: now.Add(24 * time.Hour), ConfirmedBy: "analyst", Lines: []demand.DemandLine{{ClinicID: 2, ComponentKind: "RBC", BloodGroup: "O+", DemandVolumeML: 600}}})
	send(logistics.DeliveryEstimatesPublished{SourceClinicID: 1, EstimatedAt: now, Estimates: []logistics.DeliveryEstimate{{TargetClinicID: 2, Minutes: 5}}})
	id := uuid.New()
	for range 2 {
		n, err := svc.Build(ctx, id, nil, "analyst")
		if err != nil || n.Plan == nil || len(n.Plan.Proposals) == 0 {
			t.Fatalf("план: %+v %v", n, err)
		}
	}
	var commands, plans int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE message_type='OpenObligation'`).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM replenishment_plan`).Scan(&plans); err != nil {
		t.Fatal(err)
	}
	if commands != 1 || plans != 1 {
		t.Fatalf("повтор создал команды: %d, планы: %d", commands, plans)
	}
	var before string
	if err := pool.QueryRow(ctx, `SELECT md5(string_agg(input_key||payload::text,'' ORDER BY input_key)) FROM planning_input_projection`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM planning_input_projection WHERE input_key LIKE 'unit:%'`); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.RebuildProjections(ctx, "operator-maintenance"); err != nil || n < 5 {
		t.Fatalf("восстановление: %d %v", n, err)
	}
	var after string
	if err := pool.QueryRow(ctx, `SELECT md5(string_agg(input_key||payload::text,'' ORDER BY input_key)) FROM planning_input_projection`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("проекция восстановлена не полностью")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE message_type='OpenObligation'`).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if commands != 1 {
		t.Fatal("восстановление повторило задания")
	}
	// Лишние поля отбрасываются до записи, в том числе внутри строки и территории.
	idp := authtest.New(t)
	mux := http.NewServeMux()
	api.Mount(mux, svc, nil)
	handler := auth.Authenticate(idp.Verifier(t, ""))(mux)
	raw := `{"scenario_version":"safe-1","period_start":"2026-10-24T10:00:00Z","period_end":"2026-10-24T11:00:00Z","full_name":"SECRET_PATIENT","snils":"SECRET_SNILS","patients":[{"name":"SECRET_PATIENT"}],"territory":{"name":"демо","full_name":"SECRET_PATIENT"},"lines":[{"clinic_code":"GKB01","component_kind":"RBC","blood_group":"O+","volume_ml":300,"patients":["SECRET_PATIENT"]}]}`
	// Код клиники берётся из справочника, чтобы проверка не зависела от демо-именования.
	raw = strings.ReplaceAll(raw, "GKB01", ref.Clinics[0].ClinicCode)
	request := httptest.NewRequest(http.MethodPost, "/int/forecast", strings.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+idp.Token(t, authtest.TokenOptions{Username: "C02", Roles: []auth.Role{auth.RoleService}}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 201 {
		t.Fatalf("приём прогноза: %d %s", response.Code, response.Body.String())
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT payload::text FROM emergency_forecast WHERE scenario_version='safe-1'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "SECRET_") || strings.Contains(stored, "patients") || strings.Contains(stored, "full_name") {
		t.Fatalf("лишние поля сохранились: %s", stored)
	}
	var receipt struct {
		ID uuid.UUID `json:"emergency_forecast_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	original, err := svc.Forecast(ctx, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	lineID := uuid.NewSHA1(receipt.ID, []byte("0"))
	newID, err := svc.AdjustForecast(ctx, receipt.ID, lineID, 600, original.Lines[0].ClinicCode, "Уточнение объёма", "operator")
	if err != nil || newID == receipt.ID {
		t.Fatalf("коррекция: %s %v", newID, err)
	}
	original, err = svc.Forecast(ctx, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	adjusted, err := svc.Forecast(ctx, newID)
	if err != nil {
		t.Fatal(err)
	}
	if original.Lines[0].VolumeML != 300 || adjusted.Lines[0].VolumeML != 600 {
		t.Fatal("прошлый прогноз изменён или коррекция потеряна")
	}
	if _, err := svc.Build(ctx, uuid.New(), nil, "operator"); err != nil {
		t.Fatal(err)
	}
	var cancels int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE message_type='CancelRequestObligations' AND payload->>'request_id'=$1`, id.String()).Scan(&cancels); err != nil {
		t.Fatal(err)
	}
	if cancels != 1 {
		t.Fatalf("команд отмены старого плана: %d", cancels)
	}
	obligation := uuid.New()
	version := int64(1)
	opened := transfer.ObligationOpened{ObligationID: obligation, ObligationVersion: &version, RequestID: id, Origin: transfer.OriginPlanProposal, SourceClinicID: 1, TargetClinicID: 2, ComponentKind: "RBC", RequestedQty: 1, Deadline: now.Add(time.Hour)}
	send(opened)
	confirmedVersion := int64(2)
	send(transfer.TransferConfirmed{ObligationID: obligation, ObligationVersion: &confirmedVersion, RequestID: id, Units: []uuid.UUID{uuid.New()}})
	send(opened)
	if _, err := svc.RebuildProjections(ctx, "maintenance"); err != nil {
		t.Fatal(err)
	}
	progress, err := svc.Plan(ctx, id)
	if err != nil || len(progress.Obligations) != 1 || progress.Obligations[0].ID != obligation || progress.Obligations[0].Status != "CONFIRMED" || progress.Obligations[0].Units != 1 {
		t.Fatalf("статус плана потерян или устарел: %+v %v", progress.Obligations, err)
	}
}
