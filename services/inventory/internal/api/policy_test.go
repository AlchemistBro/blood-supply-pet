package api

import (
	"bufio"
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/inventory/api"
)

var (
	pathLine   = regexp.MustCompile(`^  (/\S*):\s*$`)
	methodLine = regexp.MustCompile(`^    (get|post|put|patch|delete):\s*$`)
	opLine     = regexp.MustCompile(`^      x-bs-operation:\s*(\S+)\s*$`)
)

// specOperations читает из openapi.yaml пары «МЕТОД путь» → x-bs-operation (без YAML-библиотеки:
// разметка файла фиксирована отступами).
func specOperations(t *testing.T) (map[string]string, []string) {
	t.Helper()
	ops := map[string]string{}
	var routes []string
	var path, route string
	sc := bufio.NewScanner(bytes.NewReader(apispec.Spec))
	for sc.Scan() {
		line := sc.Text()
		if m := pathLine.FindStringSubmatch(line); m != nil {
			path = m[1]
			continue
		}
		if m := methodLine.FindStringSubmatch(line); m != nil && path != "" {
			route = strings.ToUpper(m[1]) + " " + path
			routes = append(routes, route)
			continue
		}
		if m := opLine.FindStringSubmatch(line); m != nil && route != "" {
			ops[route] = m[1]
		}
		if strings.HasPrefix(line, "components:") {
			break
		}
	}
	return ops, routes
}

// Каждая операция контракта помечена x-bs-operation, и сервер проверяет именно эту операцию матрицы прав.
func TestEveryOperationHasPolicy(t *testing.T) {
	spec, routes := specOperations(t)
	if len(routes) == 0 {
		t.Fatal("в openapi.yaml не найдено ни одной операции")
	}
	known := map[auth.Operation]bool{}
	for _, op := range auth.Operations() {
		known[op] = true
	}
	for _, route := range routes {
		op, ok := spec[route]
		if !ok {
			t.Errorf("%s: нет x-bs-operation", route)
			continue
		}
		if !known[auth.Operation(op)] {
			t.Errorf("%s: операции %s нет в матрице прав", route, op)
		}
		if got := operations[route]; string(got) != op {
			t.Errorf("%s: сервер проверяет %q, контракт объявляет %q", route, got, op)
		}
	}
	for route := range operations {
		if _, ok := spec[route]; !ok {
			t.Errorf("%s: маршрут сервера не описан в openapi.yaml", route)
		}
	}
}

// Просмотр запаса разрешён врачу, оператору, дежурному и ИТ-службе и запрещён остальным (NFR011).
func TestViewStockRoles(t *testing.T) {
	allowed := map[auth.Role]bool{auth.RolePhysician: true, auth.RoleOperator: true, auth.RoleDuty: true, auth.RoleITService: true}
	for _, r := range []auth.Role{auth.RolePhysician, auth.RoleOperator, auth.RoleAnalyst, auth.RoleDuty, auth.RoleITService, auth.RoleService} {
		if got := auth.Allowed(auth.OpViewStock, []auth.Role{r}); got != allowed[r] {
			t.Errorf("VIEW_STOCK для %s: %v, ожидали %v", r, got, allowed[r])
		}
	}
}
