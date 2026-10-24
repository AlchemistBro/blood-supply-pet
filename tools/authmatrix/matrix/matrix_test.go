package matrix_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/tools/authmatrix/matrix"
)

func readSpec(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "specs", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Нарушения соглашения x-bs-operation / x-bs-public — ошибка с указанием сервиса, метода и пути.
func TestParseSpecRejectsBrokenConvention(t *testing.T) {
	cases := []struct {
		file string
		want []string // фрагменты текста ошибки
	}{
		{"missing.yaml", []string{"svc", "DELETE /records", matrix.ExtOperation, matrix.ExtPublic}},
		{"unknown.yaml", []string{"svc", "GET /records", "NO_SUCH_OPERATION"}},
		{"both.yaml", []string{"svc", "GET /records", "одновременно"}},
		{"public-false.yaml", []string{"svc", "GET /metrics"}},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			_, err := matrix.ParseSpec("svc", readSpec(t, tc.file))
			if err == nil {
				t.Fatal("нарушение соглашения не обнаружено")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Fatalf("в ошибке нет %q: %v", w, err)
				}
			}
		})
	}
}

// Все нарушения спецификации сообщаются сразу, а не по одному за запуск.
func TestParseSpecReportsAllViolations(t *testing.T) {
	spec := []byte(`openapi: 3.0.3
paths:
  /a:
    get: { operationId: a, responses: {} }
  /b:
    post: { operationId: b, x-bs-operation: NOPE, responses: {} }
`)
	_, err := matrix.ParseSpec("svc", spec)
	if err == nil || !strings.Contains(err.Error(), "GET /a") || !strings.Contains(err.Error(), "POST /b") {
		t.Fatalf("ожидали обе ошибки, получили %v", err)
	}
}

func TestParseSpecRejectsInvalidYAML(t *testing.T) {
	if _, err := matrix.ParseSpec("svc", []byte("paths: [")); err == nil {
		t.Fatal("испорченный YAML принят")
	}
}

func TestParseSpecCollectsEndpoints(t *testing.T) {
	eps, err := matrix.ParseSpec("svc", readSpec(t, "good.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		method, path string
		op           auth.Operation
		public       bool
	}
	var got []row
	for _, e := range eps {
		if e.Service != "svc" {
			t.Fatalf("сервис %q", e.Service)
		}
		got = append(got, row{e.Method, e.Path, e.Operation, e.Public})
	}
	// Порядок: по пути, затем по методу — таблица и случаи детерминированы.
	want := []row{
		{"GET", "/healthz", "", true},
		{"GET", "/records", auth.OpReadAudit, false},
		{"POST", "/records", auth.OpWriteAudit, false},
		{"GET", "/records/{id}", auth.OpReadAudit, false},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("эндпоинты %+v, ожидали %+v", got, want)
	}
	if eps[1].OperationID != "listRecords" {
		t.Fatalf("operationId %q", eps[1].OperationID)
	}
}

func TestParseSpecEmptyPaths(t *testing.T) {
	eps, err := matrix.ParseSpec("svc", []byte("openapi: 3.0.3\npaths: {}\n"))
	if err != nil || len(eps) != 0 {
		t.Fatalf("пустой контракт: %v, %v", eps, err)
	}
}

// Ожидания строятся из policy.go: роль из матрицы — допуск, любая другая — 403. Публичные пути не проверяются.
func TestCasesFollowPolicy(t *testing.T) {
	cases, err := matrix.CasesFromSpec("svc", readSpec(t, "good.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	roles := matrix.Roles()
	if len(roles) != 6 {
		t.Fatalf("ролей %d, ожидали 6: %v", len(roles), roles)
	}
	if len(cases) != 3*len(roles) {
		t.Fatalf("случаев %d, ожидали %d (3 защищённые операции × роли)", len(cases), 3*len(roles))
	}
	allowed := map[string][]auth.Role{
		"GET /records":      {auth.RoleDuty, auth.RoleITService},
		"POST /records":     {auth.RoleService},
		"GET /records/{id}": {auth.RoleDuty, auth.RoleITService},
	}
	for _, c := range cases {
		key := c.Method + " " + c.Path
		roles, ok := allowed[key]
		if !ok {
			t.Fatalf("лишний случай %s (публичный путь не должен попадать в случаи)", key)
		}
		if want := !slices.Contains(roles, c.Role); c.WantForbidden != want {
			t.Fatalf("%s: ожидали запрет %v, получили %v", c.Name(), want, c.WantForbidden)
		}
		if c.Operation == "" || c.Service != "svc" || !strings.Contains(c.Name(), string(c.Role)) {
			t.Fatalf("неполный случай %+v (имя %q)", c, c.Name())
		}
	}
	// Каждая операция покрыта всеми ролями.
	for key := range allowed {
		n := 0
		for _, c := range cases {
			if c.Method+" "+c.Path == key {
				n++
			}
		}
		if n != len(roles) {
			t.Fatalf("%s: %d ролей из %d", key, n, len(roles))
		}
	}
}

func TestLoadRepoAndMarkdown(t *testing.T) {
	eps, err := matrix.LoadRepo(filepath.Join("testdata", "repo"))
	if err != nil {
		t.Fatal(err)
	}
	services := map[string]int{}
	for _, e := range eps {
		services[e.Service]++
	}
	if len(services) != 2 || services["alpha"] != 2 || services["beta"] != 2 {
		t.Fatalf("сервисы %v (gamma без контракта должен быть пропущен)", services)
	}
	md := string(matrix.Markdown(eps))
	// Роли по алфавиту: CLINIC_OPERATOR, DUTY_OFFICER, IT_SERVICE, PHYSICIAN, SERVICE, TERRITORIAL_ANALYST.
	for _, want := range []string{
		"| Метод и путь | Операция | CLINIC_OPERATOR | DUTY_OFFICER | IT_SERVICE | PHYSICIAN | SERVICE | TERRITORIAL_ANALYST |",
		"## alpha",
		"## beta",
		"| `POST /requests` | CREATE_REQUEST | 403 | 403 | 403 | 2xx | 403 | 403 |",
		"| `GET /requests/{id}/patient-case` | READ_PATIENT_CASE | 2xx | 403 | 403 | 2xx | 403 | 403 |",
		"| `POST /internal/units/status` | INTERNAL_UNIT_STATUS | 403 | 403 | 403 | 403 | 2xx | 403 |",
		"`GET /openapi.yaml`",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("в таблице нет %q:\n%s", want, md)
		}
	}
	if strings.Index(md, "## alpha") > strings.Index(md, "## beta") {
		t.Fatal("сервисы в таблице не упорядочены")
	}
	if !bytes.Equal(matrix.Markdown(eps), matrix.Markdown(eps)) {
		t.Fatal("таблица недетерминирована")
	}
}

func TestLoadRepoReportsBrokenService(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "services", "broken", "api")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openapi.yaml"), readSpec(t, "missing.yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := matrix.LoadRepo(root); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("ожидали ошибку сервиса broken, получили %v", err)
	}
}

func TestFillPath(t *testing.T) {
	got := matrix.FillPath("/requests/{id}/units/{unit_id}", func(p string) string { return "<" + p + ">" })
	if got != "/requests/<id>/units/<unit_id>" {
		t.Fatalf("путь %q", got)
	}
}

// Каждая роль из policy.go участвует в матрице: роль без единой операции не проверялась бы вовсе.
func TestCheckRolesAgainstPolicySource(t *testing.T) {
	root, err := matrix.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	if err := matrix.CheckRoles(filepath.Join(root, matrix.PolicyFile)); err != nil {
		t.Fatal(err)
	}
	src := []byte("package auth\ntype Role string\nconst (\n\tRoleA Role = \"PHYSICIAN\"\n\tRoleGhost Role = \"GHOST\"\n)\n")
	f := filepath.Join(t.TempDir(), "policy.go")
	if err := os.WriteFile(f, src, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := matrix.CheckRoles(f); err == nil || !strings.Contains(err.Error(), "GHOST") {
		t.Fatalf("роль без операций не обнаружена: %v", err)
	}
}

// Контракты репозитория соблюдают соглашение, а docs/architecture/auth-matrix.md соответствует им.
// Новый эндпоинт без x-bs-operation или без перегенерированной таблицы роняет этот тест.
func TestRepositoryMatrixIsConsistentAndDocumented(t *testing.T) {
	root, err := matrix.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	eps, err := matrix.LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(filepath.Join(root, matrix.DocFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(normalizeEOL(doc), matrix.Markdown(eps)) {
		t.Fatalf("%s устарел: выполните go run ./tools/authmatrix -out %s", matrix.DocFile, matrix.DocFile)
	}
}

func normalizeEOL(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")) }

func TestCasesForRepositoryService(t *testing.T) {
	cases, err := matrix.Cases("audit")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("для audit нет случаев")
	}
	if _, err := matrix.Cases("no-such-service"); err == nil {
		t.Fatal("несуществующий сервис принят")
	}
}
