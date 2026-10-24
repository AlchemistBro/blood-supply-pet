// Package matrix строит матрицу прав «роль × эндпоинт» из контрактов OpenAPI сервисов и матрицы
// libs/httpx/auth/policy.go (NFR011). Каждая операция контракта несёт расширение x-bs-operation с именем
// операции матрицы либо x-bs-public: true; новый эндпоинт без метки не проходит проверку, поэтому тест прав
// для него появляется автоматически (CONTRIBUTING.md: «новый эндпоинт → тест авторизации»).
//
// Сервисные тесты перебирают роли так:
//
//	cases, err := matrix.Cases("audit")
//	for _, c := range cases { ... запрос c.Method matrix.FillPath(c.Path, ...) с токеном роли c.Role;
//	                          c.WantForbidden → ожидаем 403, иначе не 401/403 ... }
package matrix

import (
	"cmp"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

// Расширения OpenAPI, которыми операции связываются с матрицей прав.
const (
	ExtOperation = "x-bs-operation"
	ExtPublic    = "x-bs-public"
)

// Файлы репозитория относительно корня.
const (
	PolicyFile = "libs/httpx/auth/policy.go"
	DocFile    = "docs/architecture/auth-matrix.md"
	specGlob   = "services/*/api/openapi.yaml"
)

// methods — HTTP-методы операций OpenAPI в порядке вывода.
var methods = []string{"get", "post", "put", "patch", "delete", "head", "options", "trace"}

// Endpoint — операция контракта сервиса.
type Endpoint struct {
	Service     string
	Method      string
	Path        string
	OperationID string
	// Operation — операция матрицы прав; пусто у публичного эндпоинта.
	Operation auth.Operation
	// Public — эндпоинт без проверки прав (x-bs-public: true): /healthz, /readyz, /metrics, /openapi.yaml, dev-маршруты имитаторов.
	Public bool
}

// ParseSpec читает openapi.yaml сервиса и проверяет соглашение: у каждой операции ровно одна метка —
// x-bs-operation с операцией из матрицы или x-bs-public: true. Все нарушения возвращаются вместе.
func ParseSpec(service string, data []byte) ([]Endpoint, error) {
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: разбор openapi.yaml: %w", service, err)
	}
	known := map[auth.Operation]bool{}
	for _, op := range auth.Operations() {
		known[op] = true
	}
	var out []Endpoint
	var errs []error
	for path, item := range doc.Paths {
		for _, m := range methods {
			raw, ok := item[m]
			if !ok {
				continue
			}
			method := strings.ToUpper(m)
			where := fmt.Sprintf("%s: %s %s", service, method, path)
			op, ok := raw.(map[string]any)
			if !ok {
				errs = append(errs, fmt.Errorf("%s: операция не является объектом", where))
				continue
			}
			e := Endpoint{Service: service, Method: method, Path: path}
			e.OperationID, _ = op["operationId"].(string)
			opName, hasOp := op[ExtOperation]
			public, _ := op[ExtPublic].(bool)
			switch {
			case hasOp && public:
				errs = append(errs, fmt.Errorf("%s: %s и %s: true одновременно", where, ExtOperation, ExtPublic))
				continue
			case public:
				e.Public = true
			case !hasOp:
				errs = append(errs, fmt.Errorf("%s: нет %s (операция из %s) или %s: true", where, ExtOperation, PolicyFile, ExtPublic))
				continue
			default:
				name, _ := opName.(string)
				if !known[auth.Operation(name)] {
					errs = append(errs, fmt.Errorf("%s: операция %q отсутствует в матрице %s", where, name, PolicyFile))
					continue
				}
				e.Operation = auth.Operation(name)
			}
			out = append(out, e)
		}
	}
	if len(errs) > 0 {
		slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
		return nil, errors.Join(errs...)
	}
	sortEndpoints(out)
	return out, nil
}

func sortEndpoints(eps []Endpoint) {
	slices.SortFunc(eps, func(a, b Endpoint) int {
		return cmp.Or(
			strings.Compare(a.Service, b.Service),
			strings.Compare(a.Path, b.Path),
			cmp.Compare(slices.Index(methods, strings.ToLower(a.Method)), slices.Index(methods, strings.ToLower(b.Method))),
		)
	})
}

// LoadRepo читает контракты всех сервисов services/*/api/openapi.yaml под root. Сервис без контракта пропускается.
func LoadRepo(root string) ([]Endpoint, error) {
	files, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(specGlob)))
	if err != nil {
		return nil, fmt.Errorf("поиск контрактов: %w", err)
	}
	slices.Sort(files)
	var out []Endpoint
	var errs []error
	for _, f := range files {
		service := filepath.Base(filepath.Dir(filepath.Dir(f)))
		data, err := os.ReadFile(filepath.Clean(f))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", service, err))
			continue
		}
		eps, err := ParseSpec(service, data)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, eps...)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	sortEndpoints(out)
	return out, nil
}

// Roles — все роли матрицы прав по алфавиту.
func Roles() []auth.Role {
	seen := map[auth.Role]bool{}
	var out []auth.Role
	for _, op := range auth.Operations() {
		for _, r := range auth.RolesFor(op) {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	slices.Sort(out)
	return out
}

// CheckRoles сверяет константы типа Role в исходнике policy.go с ролями матрицы: роль, которой не разрешена
// ни одна операция, выпала бы из перебора, и её отказы никто бы не проверял.
func CheckRoles(policyFile string) error {
	f, err := parser.ParseFile(token.NewFileSet(), policyFile, nil, 0)
	if err != nil {
		return fmt.Errorf("разбор %s: %w", policyFile, err)
	}
	inMatrix := map[auth.Role]bool{}
	for _, r := range Roles() {
		inMatrix[r] = true
	}
	var missing []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Role" {
				continue
			}
			for _, v := range vs.Values {
				lit, ok := v.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				role, err := strconv.Unquote(lit.Value)
				if err == nil && !inMatrix[auth.Role(role)] {
					missing = append(missing, role)
				}
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("роли без единой операции в матрице %s: %s", policyFile, strings.Join(missing, ", "))
	}
	return nil
}

// Case — одна проверка прав: запрос Method Path с токеном роли Role.
type Case struct {
	Service   string
	Method    string
	Path      string
	Operation auth.Operation
	Role      auth.Role
	// WantForbidden — по матрице роль не допущена: ожидается 403. Иначе — не 401 и не 403.
	WantForbidden bool
}

// Name — имя подтеста.
func (c Case) Name() string {
	return fmt.Sprintf("%s %s %s %s", c.Service, c.Method, c.Path, c.Role)
}

// BuildCases перебирает все роли для каждого защищённого эндпоинта. Публичные эндпоинты не перебираются.
func BuildCases(eps []Endpoint) []Case {
	roles := Roles()
	var out []Case
	for _, e := range eps {
		if e.Public {
			continue
		}
		for _, r := range roles {
			out = append(out, Case{Service: e.Service, Method: e.Method, Path: e.Path, Operation: e.Operation, Role: r,
				WantForbidden: !auth.Allowed(e.Operation, []auth.Role{r})})
		}
	}
	return out
}

// CasesFromSpec — случаи по тексту контракта (например, встроенному apispec.Spec сервиса).
func CasesFromSpec(service string, spec []byte) ([]Case, error) {
	eps, err := ParseSpec(service, spec)
	if err != nil {
		return nil, err
	}
	return BuildCases(eps), nil
}

// Cases — случаи для сервиса репозитория: контракт ищется от текущего каталога вверх до go.mod.
func Cases(service string) ([]Case, error) {
	root, err := FindRepoRoot(".")
	if err != nil {
		return nil, err
	}
	f := filepath.Join(root, "services", filepath.Base(service), "api", "openapi.yaml")
	data, err := os.ReadFile(filepath.Clean(f))
	if err != nil {
		return nil, fmt.Errorf("контракт сервиса %s: %w", service, err)
	}
	return CasesFromSpec(service, data)
}

// FindRepoRoot поднимается от dir до каталога с go.mod.
func FindRepoRoot(dir string) (string, error) {
	d, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("корень репозитория: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("go.mod не найден выше %s", dir)
		}
		d = parent
	}
}

var pathParam = regexp.MustCompile(`\{([^{}]+)\}`)

// FillPath подставляет значения параметров пути: /requests/{id} → /requests/<value("id")>.
func FillPath(path string, value func(param string) string) string {
	return pathParam.ReplaceAllStringFunc(path, func(m string) string {
		return value(m[1 : len(m)-1])
	})
}

// Markdown — таблица «роль × метод и путь» по сервисам: 2xx — допуск, 403 — отказ. Вывод детерминирован.
func Markdown(eps []Endpoint) []byte {
	roles := Roles()
	var b strings.Builder
	b.WriteString("# Матрица прав: роль × эндпоинт\n\n")
	fmt.Fprintf(&b, "<!-- Файл генерируется: go run ./tools/authmatrix -out %s. Вручную не править. -->\n\n", DocFile)
	fmt.Fprintf(&b, "Источник: расширение `%s` операций в `services/*/api/openapi.yaml` и матрица `%s` (NFR011).\n", ExtOperation, PolicyFile)
	b.WriteString("`2xx` — роль допущена к операции, `403` — отказ. Соглашение и проверки — `docs/architecture/testing.md`, раздел «Матрица прав».\n")

	var services []string
	for _, e := range eps {
		if !slices.Contains(services, e.Service) {
			services = append(services, e.Service)
		}
	}
	if len(services) == 0 {
		b.WriteString("\nКонтрактов с операциями нет.\n")
		return []byte(b.String())
	}
	for _, svc := range services {
		fmt.Fprintf(&b, "\n## %s\n\n", svc)
		var public []string
		var rows []string
		for _, e := range eps {
			if e.Service != svc {
				continue
			}
			if e.Public {
				public = append(public, fmt.Sprintf("`%s %s`", e.Method, e.Path))
				continue
			}
			cells := []string{fmt.Sprintf("`%s %s`", e.Method, e.Path), string(e.Operation)}
			for _, r := range roles {
				cell := "403"
				if auth.Allowed(e.Operation, []auth.Role{r}) {
					cell = "2xx"
				}
				cells = append(cells, cell)
			}
			rows = append(rows, "| "+strings.Join(cells, " | ")+" |")
		}
		if len(rows) > 0 {
			header := []string{"Метод и путь", "Операция"}
			sep := []string{"---", "---"}
			for _, r := range roles {
				header = append(header, string(r))
				sep = append(sep, ":---:")
			}
			b.WriteString("| " + strings.Join(header, " | ") + " |\n")
			b.WriteString("| " + strings.Join(sep, " | ") + " |\n")
			for _, row := range rows {
				b.WriteString(row + "\n")
			}
		} else {
			b.WriteString("Защищённых операций нет.\n")
		}
		if len(public) > 0 {
			if len(rows) > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "Публичные, без проверки прав: %s.\n", strings.Join(public, ", "))
		}
	}
	return []byte(b.String())
}
