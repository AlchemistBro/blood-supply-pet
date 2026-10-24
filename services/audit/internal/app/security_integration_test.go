//go:build integration

package app_test

import (
	"context"
	"testing"

	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
)

func TestSecurityReadBurstPersistsOneIncidentAndOutbox(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	for i := range 24 {
		r := record(i)
		r.Action = "READ_PATIENT_CASE"
		r.Module = auditc.ModuleMatching
		r.UserID = "physician-burst"
		r.Role = "PHYSICIAN"
		if _, err := svc.AppendDirect(ctx, r, "matching"); err != nil {
			t.Fatal(err)
		}
	}
	var incidents, messages, count int
	if err := pool.QueryRow(ctx, `SELECT count(*),coalesce(max(count),0) FROM security_incident WHERE rule='PATIENT_CASE_READ_BURST'`).Scan(&incidents, &count); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE message_type='SecurityIncidentSuspected'`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if incidents != 1 || messages != 1 || count != 21 {
		t.Fatalf("повтор сигнала или неверный порог: %d/%d/%d", incidents, messages, count)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_security_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.message_type='SecurityIncidentSuspected' THEN RAISE EXCEPTION 'offline'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_security_outbox BEFORE INSERT ON outbox FOR EACH ROW EXECUTE FUNCTION fail_security_outbox()`); err != nil {
		t.Fatal(err)
	}
	r := record(100)
	r.Action = "READ_PATIENT_CASE"
	r.Role = "PHYSICIAN"
	r.UserID = "foreign"
	clinic := int64(1)
	r.ClinicID = &clinic
	r.Details = map[string]any{"object_clinic_id": 2}
	if _, err := svc.AppendDirect(ctx, r, "matching"); err == nil {
		t.Fatal("ошибка outbox не откатила транзакцию")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_record WHERE user_id='foreign'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("аудит записан отдельно от сигнала")
	}
}

func TestVerificationDetectsDeletedTailAndSuppressesRepeatedSignal(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	for i := range 3 {
		if _, err := svc.AppendDirect(ctx, record(i), "test"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE audit_record DISABLE TRIGGER audit_record_no_update; DELETE FROM audit_record WHERE seq=3; ALTER TABLE audit_record ENABLE TRIGGER audit_record_no_update`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		rep, err := svc.Verify(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if rep.OK || rep.Broken == nil || rep.Broken.Seq != 3 {
			t.Fatalf("удалённый хвост не обнаружен: %+v", rep)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM security_incident WHERE rule='AUDIT_CHAIN_BROKEN'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("повтор проверки создал %d инцидентов", count)
	}
}
