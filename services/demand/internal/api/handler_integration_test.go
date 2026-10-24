//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth/authtest"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testinfra"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/migrations"
)

const audience = "demand"

type env struct {
	srv     *httptest.Server
	pool    *pgxpool.Pool
	ref     *refdata.Set
	analyst string
	other   string
}

func setup(t *testing.T) *env {
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
	set := refdata.MustLoad()
	if err := bspgx.WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error { return set.Seed(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
	ref, err := app.ReferenceFrom(set)
	if err != nil {
		t.Fatal(err)
	}
	c := clock.NewMutable(time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC))
	journal := messaging.NewJournal("demand", "demand", c)
	svc := app.New(pool, postgres.New(pool), journal, c, ref, slog.New(slog.NewTextHandler(io.Discard, nil)))

	mux := http.NewServeMux()
	deny := func(r *http.Request, p *auth.Principal, op auth.Operation) {
		_ = bspgx.WithTx(r.Context(), pool, func(ctx context.Context, tx pgx.Tx) error {
			return journal.Audit(ctx, tx, messaging.AuditEntry{UserID: p.Username, Role: p.PrimaryRole(),
				Action: "ACCESS_DENIED", ObjectType: "operation", ObjectID: string(op), Result: "DENIED"})
		})
	}
	api.Mount(mux, svc, deny)
	idp := authtest.New(t)
	srv := httptest.NewServer(auth.Authenticate(idp.Verifier(t, audience))(mux))
	t.Cleanup(srv.Close)
	return &env{
		srv: srv, pool: pool, ref: set,
		analyst: idp.Token(t, authtest.TokenOptions{Username: "analyst-1", Roles: []auth.Role{auth.RoleAnalyst}, Audience: audience}),
		other: idp.Token(t, authtest.TokenOptions{Username: "operator-c001", Roles: []auth.Role{auth.RoleOperator},
			ClinicID: authtest.Clinic(1), Audience: audience}),
	}
}

type uploadResult struct {
	File struct {
		ID           uuid.UUID `json:"id"`
		CheckStatus  string    `json:"check_status"`
		RejectedRows int       `json:"rejected_rows"`
		AcceptedRows int       `json:"accepted_rows"`
		DataSource   *string   `json:"data_source"`
		PeriodStart  *string   `json:"period_start"`
		FileChecksum string    `json:"file_checksum"`
		UploadedBy   string    `json:"uploaded_by"`
	} `json:"file"`
	FileError      *string `json:"file_error"`
	ShareVersionNo *int64  `json:"share_version_no"`
	RowErrors      []struct {
		RowNo  int    `json:"row_no"`
		Reason string `json:"reason"`
	} `json:"row_errors"`
}

// reply — прочитанный ответ; тело закрывается в request.
type reply struct {
	StatusCode int
	Body       []byte
}

func (e *env) request(t *testing.T, method, path, token string, body io.Reader, ctype string) reply {
	t.Helper()
	if body == nil {
		body = http.NoBody
	}
	req, err := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{StatusCode: resp.StatusCode, Body: b}
}

func (e *env) upload(t *testing.T, kind, name string, content []byte) reply {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("file_kind", kind); err != nil {
		t.Fatal(err)
	}
	fw, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return e.request(t, http.MethodPost, "/files", e.analyst, &buf, w.FormDataContentType())
}

func decode[T any](t *testing.T, resp reply) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(resp.Body, &v); err != nil {
		t.Fatalf("тело ответа %q: %v", resp.Body, err)
	}
	return v
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (e *env) mustUpload(t *testing.T, kind string, f testdata.CSVFile, status string) uploadResult {
	t.Helper()
	resp := e.upload(t, kind, f.Name, f.Content)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("загрузка %s: статус %d: %s", f.Name, resp.StatusCode, resp.Body)
	}
	res := decode[uploadResult](t, resp)
	if res.File.CheckStatus != status {
		t.Fatalf("%s: статус проверки %s, ожидали %s: %+v", f.Name, res.File.CheckStatus, status, res.RowErrors)
	}
	return res
}

// Повтор того же файла (SHA-256) отклоняется 409 со ссылкой на первый, второй записи нет, отказ в аудите.
func TestUploadSameFileTwiceRejected(t *testing.T) {
	e := setup(t)
	f := testdata.ClinicShareCSV(e.ref, testdata.DefaultCSVConfig())
	first := e.mustUpload(t, "CLINIC_SHARE", f, "ACCEPTED")
	if first.ShareVersionNo == nil || *first.ShareVersionNo != 1 {
		t.Fatalf("версия долей: %v", first.ShareVersionNo)
	}

	resp := e.upload(t, "CLINIC_SHARE", "другое_имя.csv", f.Content)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("повтор: статус %d, ожидали 409", resp.StatusCode)
	}
	p := decode[map[string]any](t, resp)
	if p["existing_file_id"] != first.File.ID.String() || p["title"] != "Этот файл уже загружен" {
		t.Fatalf("ответ на повтор: %v", p)
	}
	if n := e.count(t, `SELECT count(*) FROM statistics_file`); n != 1 {
		t.Fatalf("файлов %d, ожидали 1", n)
	}
	if n := e.count(t, `SELECT count(*) FROM clinic_share_version`); n != 1 {
		t.Fatalf("версий долей %d, ожидали 1", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_local WHERE action = 'FILE_REJECTED' AND object_id = $1 AND details->>'reason' = 'DUPLICATE'`,
		first.File.ID.String()); n != 1 {
		t.Fatalf("аудит повтора: %d записей", n)
	}
}

// PARTIAL: принятые строки сохранены, отклонённые — в file_row_error с номерами строк файла.
func TestPartialFileStoresAcceptedRowsAndErrors(t *testing.T) {
	e := setup(t)
	cfg := testdata.DefaultCSVConfig()
	cfg.Broken = true
	f := testdata.ConsumptionHistoryCSV(e.ref, cfg)
	res := e.mustUpload(t, "CONSUMPTION_HISTORY", f, "PARTIAL")
	accepted := f.Rows - len(f.BadRows)
	if res.File.RejectedRows != len(f.BadRows) || res.File.AcceptedRows != accepted {
		t.Fatalf("принято %d, отклонено %d; ожидали %d и %d", res.File.AcceptedRows, res.File.RejectedRows, accepted, len(f.BadRows))
	}
	if n := e.count(t, `SELECT count(*) FROM consumption_history WHERE statistics_file_id = $1`, res.File.ID); n != accepted {
		t.Fatalf("сохранено строк истории %d, ожидали %d", n, accepted)
	}

	resp := e.request(t, http.MethodGet, "/files/"+res.File.ID.String()+"/errors", e.analyst, nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ошибки файла: статус %d", resp.StatusCode)
	}
	list := decode[struct {
		Items []struct {
			RowNo  int    `json:"row_no"`
			Reason string `json:"reason"`
		} `json:"items"`
	}](t, resp)
	if len(list.Items) != len(f.BadRows) {
		t.Fatalf("ошибок %d, ожидали %d: %+v", len(list.Items), len(f.BadRows), list.Items)
	}
	for i, row := range f.BadRows {
		if list.Items[i].RowNo != row || list.Items[i].Reason == "" {
			t.Fatalf("ошибка %d: %+v, ожидали строку %d", i, list.Items[i], row)
		}
	}

	resp = e.request(t, http.MethodGet, "/files/"+res.File.ID.String(), e.analyst, nil, "")
	got := decode[map[string]any](t, resp)
	if got["check_status"] != "PARTIAL" || got["accepted_rows"] != float64(accepted) || got["uploaded_by"] != "analyst-1" {
		t.Fatalf("карточка файла: %v", got)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_local WHERE action = 'FILE_UPLOADED' AND object_id = $1
		AND details->>'check_status' = 'PARTIAL' AND NOT (details ? 'file_name')`, res.File.ID.String()); n != 1 {
		t.Fatalf("аудит загрузки: %d записей", n)
	}
}

// REJECTED: файл сохраняется для истории загрузок, но ни одной строки данных не сохраняется.
func TestRejectedFileStoresNoRows(t *testing.T) {
	e := setup(t)

	// Заголовок не по формату: отклонён целиком, причина — строкой 1, источника и периода нет.
	res := e.mustUpload(t, "DISTRICT_PROFILE", testdata.CSVFile{Name: "bad.csv", Content: []byte("patient_name;population\nx;1\n")}, "REJECTED")
	if res.FileError == nil || !strings.Contains(*res.FileError, "неизвестная колонка") {
		t.Fatalf("причина: %v", res.FileError)
	}
	if res.File.DataSource != nil || res.File.PeriodStart != nil {
		t.Fatalf("у отклонённого целиком файла есть сводка: %+v", res.File)
	}
	if len(res.RowErrors) != 1 || res.RowErrors[0].RowNo != 1 {
		t.Fatalf("ошибки: %+v", res.RowErrors)
	}

	// Все строки отклонены: сумма долей района 1,2.
	shares := testdata.CSVFile{Name: "shares.csv", Content: []byte(
		"district_code;clinic_code;clinic_share;basis;data_source;period_start;period_end\n" +
			"D01;C001;0,7;ДЕМО;ДЕМО;2026-01-01;2026-12-31\n" +
			"D01;C002;0,5;ДЕМО;ДЕМО;2026-01-01;2026-12-31\n")}
	res2 := e.mustUpload(t, "CLINIC_SHARE", shares, "REJECTED")
	if res2.ShareVersionNo != nil || res2.File.RejectedRows != 2 {
		t.Fatalf("отклонённые доли: %+v", res2)
	}

	if n := e.count(t, `SELECT count(*) FROM district_profile`); n != 0 {
		t.Fatalf("профилей %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM clinic_share_version`); n != 0 {
		t.Fatalf("версий долей %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM statistics_file WHERE check_status = 'REJECTED'`); n != 2 {
		t.Fatalf("отклонённых файлов %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_local WHERE action = 'FILE_REJECTED'`); n != 2 {
		t.Fatalf("аудит отказов: %d", n)
	}
	if resp := e.request(t, http.MethodGet, "/shares/current", e.analyst, nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("действующих долей нет: статус %d", resp.StatusCode)
	}
}

func TestValidProfileAndSharesReadable(t *testing.T) {
	e := setup(t)
	cfg := testdata.DefaultCSVConfig()
	res := e.mustUpload(t, "DISTRICT_PROFILE", testdata.DistrictProfileCSV(e.ref, cfg), "ACCEPTED")
	if n := e.count(t, `SELECT count(*) FROM district_profile WHERE statistics_file_id = $1`, res.File.ID); n != len(e.ref.Districts) {
		t.Fatalf("профилей %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM district_group_share`); n != len(e.ref.Districts)*len(e.ref.BloodGroups) {
		t.Fatalf("долей групп %d", n)
	}
	// Доли групп сохраняются точно, numeric(5,4): сумма по району ровно 1.
	if n := e.count(t, `SELECT count(*) FROM (SELECT district_profile_id FROM district_group_share
		GROUP BY district_profile_id HAVING sum(group_share) <> 1) x`); n != 0 {
		t.Fatalf("районов с суммой долей не 1: %d", n)
	}

	sharesFile := testdata.ClinicShareCSV(e.ref, cfg)
	e.mustUpload(t, "CLINIC_SHARE", sharesFile, "ACCEPTED")
	resp := e.request(t, http.MethodGet, "/shares/current", e.analyst, nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("действующие доли: статус %d", resp.StatusCode)
	}
	cur := decode[struct {
		Version struct {
			VersionNo int64  `json:"version_no"`
			Basis     string `json:"basis"`
			ValidFrom string `json:"valid_from"`
		} `json:"version"`
		Shares []struct {
			DistrictID  int64   `json:"district_id"`
			ClinicShare float64 `json:"clinic_share"`
		} `json:"shares"`
	}](t, resp)
	if cur.Version.VersionNo != 1 || cur.Version.ValidFrom != "2026-01-01" || len(cur.Shares) != sharesFile.Rows {
		t.Fatalf("действующие доли: %+v", cur.Version)
	}
	sums := map[int64]float64{}
	for _, s := range cur.Shares {
		sums[s.DistrictID] += s.ClinicShare
	}
	for d, s := range sums {
		if s < 0.99995 || s > 1.00005 {
			t.Fatalf("район %d: сумма долей %.4f", d, s)
		}
	}

	// Новый файл долей — новая версия (FR046).
	cfg2 := cfg
	cfg2.Seed++
	second := e.mustUpload(t, "CLINIC_SHARE", testdata.ClinicShareCSV(e.ref, cfg2), "ACCEPTED")
	if second.ShareVersionNo == nil || *second.ShareVersionNo != 2 {
		t.Fatalf("вторая версия: %v", second.ShareVersionNo)
	}
	versions := decode[struct {
		Items []struct {
			VersionNo int64 `json:"version_no"`
		} `json:"items"`
	}](t, e.request(t, http.MethodGet, "/shares/versions", e.analyst, nil, ""))
	if len(versions.Items) != 2 || versions.Items[0].VersionNo != 2 {
		t.Fatalf("версии: %+v", versions.Items)
	}

	files := decode[struct {
		Items []struct {
			FileKind string `json:"file_kind"`
		} `json:"items"`
	}](t, e.request(t, http.MethodGet, "/files?file_kind=CLINIC_SHARE", e.analyst, nil, ""))
	if len(files.Items) != 2 {
		t.Fatalf("файлов долей %d", len(files.Items))
	}
}

// Исправленная история за те же периоды заменяет прежнюю; частичное пересечение и замена истории,
// вошедшей в оценку, отклоняются построчно, без падения на UNIQUE ПР6.
func TestCorrectedHistoryReplacesPreviousUnlessUsed(t *testing.T) {
	e := setup(t)
	header := "component_kind_code;period_start;period_end;consumed_volume_ml;data_source\n"
	e.mustUpload(t, "CONSUMPTION_HISTORY", testdata.CSVFile{Name: "h1.csv", Content: []byte(header +
		"RBC;2025-01-01;2025-01-31;1000;ДЕМО\nRBC;2025-02-01;2025-02-28;2000;ДЕМО\n")}, "ACCEPTED")

	// Исправление января: та же запись заменяется, февраль не трогается.
	e.mustUpload(t, "CONSUMPTION_HISTORY", testdata.CSVFile{Name: "h2.csv", Content: []byte(header +
		"RBC;2025-01-01;2025-01-31;1500;ДЕМО\n")}, "ACCEPTED")
	if n := e.count(t, `SELECT count(*) FROM consumption_history WHERE component_kind_code = 'RBC'`); n != 2 {
		t.Fatalf("записей истории %d, ожидали 2", n)
	}
	if n := e.count(t, `SELECT consumed_volume_ml::int FROM consumption_history WHERE period_start = '2025-01-01'`); n != 1500 {
		t.Fatalf("январь не заменён: %d", n)
	}

	// Частичное пересечение отклоняется.
	partial := e.mustUpload(t, "CONSUMPTION_HISTORY", testdata.CSVFile{Name: "h3.csv", Content: []byte(header +
		"RBC;2025-01-15;2025-02-15;10;ДЕМО\nFFP;2025-01-01;2025-01-31;10;ДЕМО\n")}, "PARTIAL")
	if len(partial.RowErrors) != 1 || partial.RowErrors[0].RowNo != 2 || !strings.Contains(partial.RowErrors[0].Reason, "частично пересекается") {
		t.Fatalf("частичное пересечение: %+v", partial.RowErrors)
	}

	// Февраль вошёл в версию оценки — его замена отклоняется.
	profile := e.mustUpload(t, "DISTRICT_PROFILE", testdata.DistrictProfileCSV(e.ref, testdata.DefaultCSVConfig()), "ACCEPTED")
	shares := e.mustUpload(t, "CLINIC_SHARE", testdata.ClinicShareCSV(e.ref, testdata.DefaultCSVConfig()), "ACCEPTED")
	ctx := context.Background()
	estimateID := uuid.Must(uuid.NewV7())
	if _, err := e.pool.Exec(ctx, `INSERT INTO demand_estimate (demand_estimate_id, version_no, period_start, period_end, profile_file_id,
		clinic_share_version_id, estimate_status_code, calculated_at)
		SELECT $1, 1, '2026-01-01', '2026-01-31', $2, clinic_share_version_id, 'CALCULATED', now()
		FROM clinic_share_version WHERE statistics_file_id = $3`, estimateID, profile.File.ID, shares.File.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO estimate_history_link (demand_estimate_id, consumption_history_id)
		SELECT $1, consumption_history_id FROM consumption_history WHERE period_start = '2025-02-01'`, estimateID); err != nil {
		t.Fatal(err)
	}
	used := e.mustUpload(t, "CONSUMPTION_HISTORY", testdata.CSVFile{Name: "h4.csv", Content: []byte(header +
		"RBC;2025-02-01;2025-02-28;9999;ДЕМО\n")}, "REJECTED")
	if len(used.RowErrors) != 1 || !strings.Contains(used.RowErrors[0].Reason, "уже вошла в версию оценки") {
		t.Fatalf("замена использованной истории: %+v", used.RowErrors)
	}
	if n := e.count(t, `SELECT consumed_volume_ml::int FROM consumption_history WHERE period_start = '2025-02-01'`); n != 2000 {
		t.Fatalf("февраль изменён: %d", n)
	}
}

// Роль без права получает 403 на каждом эндпоинте, отказ записан в аудит (NFR008).
func TestEveryEndpointForbiddenForOperator(t *testing.T) {
	e := setup(t)
	id := uuid.Must(uuid.NewV7()).String()
	paths := []struct{ method, path string }{
		{http.MethodPost, "/files"}, {http.MethodGet, "/files"}, {http.MethodGet, "/files/" + id},
		{http.MethodGet, "/files/" + id + "/errors"}, {http.MethodGet, "/history"},
		{http.MethodGet, "/shares/versions"}, {http.MethodGet, "/shares/current"},
	}
	for _, p := range paths {
		var body io.Reader
		ctype := ""
		if p.method == http.MethodPost {
			var buf bytes.Buffer
			w := multipart.NewWriter(&buf)
			_ = w.WriteField("file_kind", "CLINIC_SHARE")
			_ = w.Close()
			body, ctype = &buf, w.FormDataContentType()
		}
		resp := e.request(t, p.method, p.path, e.other, body, ctype)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s: статус %d, ожидали 403", p.method, p.path, resp.StatusCode)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM audit_local WHERE action = 'ACCESS_DENIED' AND user_id = 'operator-c001'
		AND object_id = 'UPLOAD_STATISTICS'`); n != len(paths) {
		t.Fatalf("отказов в аудите %d, ожидали %d", n, len(paths))
	}
	if n := e.count(t, `SELECT count(*) FROM statistics_file`); n != 0 {
		t.Fatalf("оператор загрузил файл: %d", n)
	}
}

func TestHistoryEndpointFiltersByKind(t *testing.T) {
	e := setup(t)
	f := testdata.ConsumptionHistoryCSV(e.ref, testdata.DefaultCSVConfig())
	e.mustUpload(t, "CONSUMPTION_HISTORY", f, "ACCEPTED")
	all := decode[struct {
		Items []struct {
			ComponentKindCode string `json:"component_kind_code"`
			HistorySource     string `json:"history_source"`
		} `json:"items"`
	}](t, e.request(t, http.MethodGet, "/history", e.analyst, nil, ""))
	if len(all.Items) != f.Rows || all.Items[0].HistorySource != "FILE" {
		t.Fatalf("история: %d записей", len(all.Items))
	}
	rbc := decode[struct {
		Items []struct {
			ComponentKindCode string `json:"component_kind_code"`
		} `json:"items"`
	}](t, e.request(t, http.MethodGet, "/history?component_kind_code=RBC", e.analyst, nil, ""))
	if len(rbc.Items) != 12 {
		t.Fatalf("RBC: %d записей", len(rbc.Items))
	}
	for _, it := range rbc.Items {
		if it.ComponentKindCode != "RBC" {
			t.Fatalf("фильтр по виду: %+v", it)
		}
	}
	if resp := e.request(t, http.MethodGet, "/files/"+uuid.Must(uuid.NewV7()).String(), e.analyst, nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("несуществующий файл: статус %d", resp.StatusCode)
	}
}
