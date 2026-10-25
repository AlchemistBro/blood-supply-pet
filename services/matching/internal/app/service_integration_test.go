//go:build integration

package app_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/k1seal/opener"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testinfra"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/domain"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/migrations"
)

type harness struct {
	svc  *app.Service
	pool *pgxpool.Pool
	ctx  context.Context
	at   time.Time
}

func (h harness) send(t *testing.T, event contracts.Payload) {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Handle(h.ctx, messaging.Message{ID: uuid.New(), Type: event.MessageType(), SchemaVersion: 1, OccurredAt: h.at, Payload: raw}); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) harness {
	t.Helper()
	ctx := context.Background()
	pool, err := bspgx.Connect(ctx, testinfra.Postgres(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := bspgx.Migrate(ctx, pool, messaging.Migrations, refdata.Migrations, migrations.FS); err != nil {
		t.Fatal(err)
	}
	ref := refdata.MustLoad()
	if err := bspgx.WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error { return ref.Seed(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	clk := clock.NewMutable(now)
	keys, err := opener.ParseKeyring("test:" + base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	svc := app.New(pool, postgres.New(pool), messaging.NewJournal("matching", "matching", clk), ref, clk, keys)
	if err := svc.SeedRules(ctx); err != nil {
		t.Fatal(err)
	}
	send := func(event contracts.Payload) {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Handle(ctx, messaging.Message{ID: uuid.New(), Type: event.MessageType(), SchemaVersion: 1, OccurredAt: now, Payload: raw}); err != nil {
			t.Fatal(err)
		}
	}
	send(inventory.UnitRegistered{UnitID: uuid.New(), ClinicID: 2, ComponentKind: "RBC", BloodGroup: "O+", RhPositive: true, Phenotype: "C+K-", VolumeML: 300, ExpiresAt: now.Add(30 * 24 * time.Hour), Status: "IN_STOCK", Version: 1})
	send(inventory.StockSnapshotPublished{ClinicID: 2, ComponentKind: "RBC", BloodGroup: "O+", UnitCount: 1, VolumeML: 300, Watermark: 1, SyncedAt: now})
	send(inventory.ProtectedVolumePublished{ClinicID: 2, ComponentKind: "RBC", VolumeUnits: 0, Version: 1, Reason: "демо"})
	send(logistics.DeliveryEstimatesPublished{SourceClinicID: 2, EstimatedAt: now, Estimates: []logistics.DeliveryEstimate{{TargetClinicID: 1, Minutes: 5}}})
	return harness{svc: svc, pool: pool, ctx: ctx, at: now}
}

func TestRequestSummaryExcludesLargeVariantsWithoutChangingDetail(t *testing.T) {
	h := setup(t)
	actor := app.Actor{User: "doctor-1", ClinicID: 1, Roles: []string{"PHYSICIAN"}}
	r := domain.Request{RequestID: uuid.New(), ClinicID: 1, ComponentKind: "RBC", RequestedUnits: 1, Urgency: "PLANNED", BloodGroup: "O+", Phenotype: "K-"}
	n, err := h.svc.Create(h.ctx, r, "CASE_SECRET", actor)
	if err != nil || len(n.Result.Variants) != 1 {
		t.Fatalf("создание заявки: %v", err)
	}
	n.Result.Variants[0].Explanation = strings.Repeat("x", 9<<20)
	raw, err := json.Marshal(n.Result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(h.ctx, `UPDATE clinical_request SET match_result=$2 WHERE clinical_request_id=$1`, r.RequestID, raw); err != nil {
		t.Fatal(err)
	}
	full, err := h.svc.Requests(h.ctx, actor, false)
	if err != nil || len(full) != 1 || len(full[0].Result.Variants) != 1 || len(full[0].Result.Variants[0].Explanation) != 9<<20 {
		t.Fatalf("полный список потерял варианты: %v", err)
	}
	compact, err := h.svc.Requests(h.ctx, actor, true)
	if err != nil || len(compact) != 1 {
		t.Fatalf("компактный список: %v", err)
	}
	if len(compact[0].Result.Variants) != 0 || compact[0].Request.RequestID != r.RequestID || compact[0].Result.StockWatermark != n.Result.StockWatermark {
		t.Fatal("компактный список должен сохранять заявку и водяной знак без вариантов")
	}
	detail, err := h.svc.Request(h.ctx, r.RequestID, actor)
	if err != nil || len(detail.Result.Variants) != 1 || len(detail.Result.Variants[0].Explanation) != 9<<20 {
		t.Fatalf("полная карточка изменена: %v", err)
	}
	other := app.Actor{User: "other-doctor", ClinicID: 2, Roles: []string{"PHYSICIAN"}}
	if items, err := h.svc.Requests(h.ctx, other, true); err != nil || len(items) != 0 {
		t.Fatalf("компактный список раскрыл чужую клинику: %v", err)
	}
}

func TestRequestSelectionSealsCaseAndIsIdempotent(t *testing.T) {
	h := setup(t)
	actor := app.Actor{User: "doctor-1", ClinicID: 1, Roles: []string{"PHYSICIAN"}}
	r := domain.Request{RequestID: uuid.New(), ClinicID: 1, ComponentKind: "RBC", RequestedUnits: 1, Urgency: "PLANNED", BloodGroup: "O+", Phenotype: "K-"}
	n, err := h.svc.Create(h.ctx, r, "CASE_SECRET", actor)
	if err != nil || len(n.Result.Variants) != 1 {
		t.Fatalf("заявка: %+v %v", n, err)
	}
	for range 2 {
		if _, err := h.svc.Create(h.ctx, r, "CASE_SECRET", actor); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.svc.Create(h.ctx, r, "OTHER_CASE", actor); !errors.Is(err, app.ErrConflict) {
		t.Fatalf("ключ перепривязан к иному случаю: %v", err)
	}
	choice := uuid.New()
	variant := app.VariantID(r.RequestID, 1)
	for range 2 {
		if _, err := h.svc.Select(h.ctx, r.RequestID, variant, choice, actor); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var raw string
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM outbox WHERE message_type='OpenObligation'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("создано команд выбора: %d", count)
	}
	if err := h.pool.QueryRow(h.ctx, `SELECT payload::text FROM outbox WHERE message_type='OpenObligation'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "CASE_SECRET") || !strings.Contains(raw, "patient_case_sealed") {
		t.Fatal("открытый номер ушёл в outbox или конверт потерян")
	}
	var patient string
	if err := h.pool.QueryRow(h.ctx, `SELECT patient_case_sealed FROM clinical_request_patient`).Scan(&patient); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(patient, "CASE_SECRET") {
		t.Fatal("номер сохранён открытым")
	}
	if plain, err := h.svc.PatientCase(h.ctx, r.RequestID, actor); err != nil || plain != "CASE_SECRET" {
		t.Fatalf("свой номер не прочитан: %v", err)
	}
	foreign := app.Actor{User: "doctor-2", ClinicID: 1, Roles: []string{"PHYSICIAN"}}
	if plain, err := h.svc.PatientCase(h.ctx, r.RequestID, foreign); !errors.Is(err, app.ErrForbidden) || plain != "" {
		t.Fatal("чужой врач прочитал номер")
	}
	if _, err := h.pool.Exec(h.ctx, `CREATE FUNCTION fail_read_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='READ_PATIENT_CASE' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_read BEFORE INSERT ON audit_local FOR EACH ROW EXECUTE FUNCTION fail_read_audit()`); err != nil {
		t.Fatal(err)
	}
	if plain, err := h.svc.PatientCase(h.ctx, r.RequestID, actor); err == nil || plain != "" {
		t.Fatal("номер выдан при недоступном аудите")
	}
}

func TestShortfallCreatesOnlyRemainingQuantityAtAnotherSource(t *testing.T) {
	h := setup(t)
	thirdUnit := uuid.New()
	h.send(t, inventory.UnitRegistered{UnitID: thirdUnit, ClinicID: 3, ComponentKind: "RBC", BloodGroup: "O+", RhPositive: true, Phenotype: "K-", VolumeML: 300, ExpiresAt: h.at.Add(30 * 24 * time.Hour), Status: "IN_STOCK", Version: 1})
	h.send(t, inventory.ProtectedVolumePublished{ClinicID: 3, ComponentKind: "RBC", VolumeUnits: 0, Version: 2, Reason: "демо"})
	h.send(t, logistics.DeliveryEstimatesPublished{SourceClinicID: 3, EstimatedAt: h.at, Estimates: []logistics.DeliveryEstimate{{TargetClinicID: 1, Minutes: 10}}})
	actor := app.Actor{User: "doctor", ClinicID: 1, Roles: []string{"PHYSICIAN"}}
	r := domain.Request{RequestID: uuid.New(), ClinicID: 1, ComponentKind: "RBC", RequestedUnits: 2, Urgency: "PLANNED", BloodGroup: "O+", Phenotype: "K-"}
	n, err := h.svc.Create(h.ctx, r, "CASE", actor)
	if err != nil {
		t.Fatal(err)
	}
	var chosen domain.Variant
	for _, v := range n.Result.Variants {
		if v.SourceClinicID == 2 {
			chosen = v
		}
	}
	if chosen.Rank == 0 {
		t.Fatal("нет первого источника")
	}
	if _, err := h.svc.Select(h.ctx, r.RequestID, app.VariantID(r.RequestID, chosen.Rank), uuid.New(), actor); err != nil {
		t.Fatal(err)
	}
	obligationID := uuid.New()
	version := int64(1)
	h.send(t, transfer.ObligationOpened{ObligationID: obligationID, ObligationVersion: &version, RequestID: r.RequestID, Origin: transfer.OriginDoctorChoice, SourceClinicID: 2, TargetClinicID: 1, ComponentKind: "RBC", RequestedQty: 2, Urgency: transfer.UrgencyPlanned, Deadline: h.at.Add(time.Hour)})
	version = 2
	source, target := int64(2), int64(1)
	h.send(t, transfer.TransferConfirmed{ObligationID: obligationID, ObligationVersion: &version, RequestID: r.RequestID, SourceClinicID: &source, TargetClinicID: &target, Units: chosen.UnitIDs, Quantity: 1, Operator: "operator"})
	event := transfer.TransferShortfallRecorded{ObligationID: obligationID, ObligationVersion: &version, RequestID: r.RequestID, SourceClinicID: &source, TargetClinicID: &target, ComponentKind: "RBC", Shortfall: 1}
	for range 2 {
		h.send(t, event)
	}
	rows, err := h.pool.Query(h.ctx, `SELECT payload FROM outbox WHERE message_type='OpenObligation' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var commands []transfer.OpenObligation
	for rows.Next() {
		var raw []byte
		var command transfer.OpenObligation
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &command); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 || commands[0].RequestedQty != 2 || commands[1].RequestedQty != 1 || commands[1].SourceClinicID != 3 {
		t.Fatalf("неверный остаток или повтор источника: %+v", commands)
	}
}
