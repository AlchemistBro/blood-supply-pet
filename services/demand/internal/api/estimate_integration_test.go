//go:build integration

package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
)

func TestEstimatePublishesOnceAndFreezesHistory(t *testing.T) {
	e := setup(t)
	profile := e.mustUpload(t, "DISTRICT_PROFILE", testdata.DistrictProfileCSV(e.ref, testdata.DefaultCSVConfig()), "ACCEPTED")
	shares := e.mustUpload(t, "CLINIC_SHARE", testdata.ClinicShareCSV(e.ref, testdata.DefaultCSVConfig()), "ACCEPTED")
	e.mustUpload(t, "CONSUMPTION_HISTORY", testdata.ConsumptionHistoryCSV(e.ref, testdata.DefaultCSVConfig()), "ACCEPTED")
	var shareID uuid.UUID
	if err := e.pool.QueryRow(t.Context(), `SELECT clinic_share_version_id FROM clinic_share_version WHERE statistics_file_id=$1`, shares.File.ID).Scan(&shareID); err != nil {
		t.Fatal(err)
	}
	rows, err := e.pool.Query(t.Context(), `SELECT consumption_history_id FROM consumption_history`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	body, _ := json.Marshal(map[string]any{"profile_file_id": profile.File.ID, "clinic_share_version_id": shareID, "history_ids": ids, "period_start": "2026-11-01", "period_end": "2026-11-30"})
	if denied := e.request(t, http.MethodPost, "/estimates", e.other, bytes.NewReader(body), "application/json"); denied.StatusCode != 403 {
		t.Fatalf("чужая роль рассчитала оценку: %d", denied.StatusCode)
	}
	created := e.request(t, http.MethodPost, "/estimates", e.analyst, bytes.NewReader(body), "application/json")
	if created.StatusCode != 200 {
		t.Fatalf("расчёт: %d %s", created.StatusCode, created.Body)
	}
	result := decode[struct {
		ID uuid.UUID `json:"demand_estimate_id"`
	}](t, created)
	for range 2 {
		if response := e.request(t, http.MethodPost, "/estimates/"+result.ID.String()+"/publish", e.analyst, bytes.NewBufferString(`{}`), "application/json"); response.StatusCode != 200 {
			t.Fatalf("публикация: %d %s", response.StatusCode, response.Body)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM outbox WHERE message_type='DemandEstimatePublished'`); n != 1 {
		t.Fatalf("публикаций %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM estimate_history_link WHERE demand_estimate_id=$1`, result.ID); n != len(ids) {
		t.Fatalf("версии истории не сохранены: %d", n)
	}
	if response := e.request(t, http.MethodPost, "/estimates/"+result.ID.String()+"/reject", e.analyst, bytes.NewBufferString(`{"reason":"исправить входы"}`), "application/json"); response.StatusCode != 422 {
		t.Fatalf("опубликованная версия изменена: %d", response.StatusCode)
	}
	created = e.request(t, http.MethodPost, "/estimates", e.analyst, bytes.NewReader(body), "application/json")
	if created.StatusCode != 200 {
		t.Fatalf("вторая версия: %d", created.StatusCode)
	}
	draft := decode[struct {
		ID uuid.UUID `json:"demand_estimate_id"`
	}](t, created)
	for range 2 {
		if response := e.request(t, http.MethodPost, "/estimates/"+draft.ID.String()+"/reject", e.analyst, bytes.NewBufferString(`{"reason":"исправить входы"}`), "application/json"); response.StatusCode != 200 {
			t.Fatalf("отклонение: %d %s", response.StatusCode, response.Body)
		}
	}
	if response := e.request(t, http.MethodPost, "/estimates/"+draft.ID.String()+"/publish", e.analyst, bytes.NewBufferString(`{}`), "application/json"); response.StatusCode != 422 {
		t.Fatalf("отклонённая версия опубликована: %d", response.StatusCode)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_local WHERE action='ESTIMATE_REJECTED'`); n != 1 {
		t.Fatalf("повтор отклонения породил %d записей", n)
	}

	before := map[string]int{}
	for _, table := range []string{"demand_estimate", "estimate_history_link", "clinic_demand", "outbox", "audit_local"} {
		before[table] = e.count(t, "SELECT count(*) FROM "+table)
	}
	if _, err := e.pool.Exec(t.Context(), `CREATE FUNCTION fail_estimate_line() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.clinic_id=2 THEN RAISE EXCEPTION 'demo failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_estimate_line BEFORE INSERT ON clinic_demand FOR EACH ROW EXECUTE FUNCTION fail_estimate_line()`); err != nil {
		t.Fatal(err)
	}
	if response := e.request(t, http.MethodPost, "/estimates", e.analyst, bytes.NewReader(body), "application/json"); response.StatusCode != 500 {
		t.Fatalf("ошибка строки не отклонила расчёт: %d", response.StatusCode)
	}
	for table, count := range before {
		if got := e.count(t, "SELECT count(*) FROM "+table); got != count {
			t.Fatalf("ошибка пакета оставила изменения %s: %d → %d", table, count, got)
		}
	}
}
