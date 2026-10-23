// Package archtest проверяет правила зависимостей репозитория (CONTRIBUTING.md, «Архитектурные правила»):
//   - libs/** не импортирует services/**;
//   - services/<a>/** не импортирует services/<b>/** (строже, чем правило internal компилятора);
//   - internal/domain не зависит от pgx, Kafka, HTTP и SQL;
//   - клиент Kafka (franz-go) импортируется только в libs/messaging, libs/testinfra, tools и tests;
//   - код с ключом номера случая (libs/k1seal/opener) прямо или транзитивно достижим только из matching,
//     inventory и libs/k1seal; список подпакетов libs/k1seal закрыт (ADR-0020).
package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const module = "github.com/AlchemistBro/blood-supply-pet"

func loadAll(t *testing.T) []*packages.Package {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedImports | packages.NeedDeps, Dir: root, Tests: true}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		t.Fatal(err)
	}
	return pkgs
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

func serviceOf(path string) string {
	const prefix = module + "/services/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.Index(rest, "/"); i >= 0 {
		return rest[:i]
	}
	return rest
}

// k1sealAllowed — подпакеты libs/k1seal. Список закрыт: новый подпакет без правки правила — нарушение,
// иначе в него можно вынести расшифровку в обход opener.
var k1sealAllowed = map[string]bool{
	module + "/libs/k1seal":        true, // тип конверта Sealed, без ключей и криптографии
	module + "/libs/k1seal/opener": true, // связка ключей, шифрование и расшифровка
}

// k1sealViolations проверяет правило ADR-0020 для пакета pkg с транзитивными импортами deps: код с ключом
// номера случая (любой подпакет libs/k1seal, кроме корня) достижим только из services/matching,
// services/inventory и самой libs/k1seal. Внешние тесты (…_test) и тестовые main (….test) проверяются как
// пакет, к которому относятся.
func k1sealViolations(pkg string, deps map[string]bool) []string {
	const root = module + "/libs/k1seal"
	norm := strings.TrimSuffix(strings.TrimSuffix(pkg, ".test"), "_test")
	if norm == root || strings.HasPrefix(norm, root+"/") {
		if !k1sealAllowed[norm] {
			return []string{fmt.Sprintf("%s: неизвестный подпакет libs/k1seal; список закрыт правилом archtest (ADR-0020)", pkg)}
		}
		return nil
	}
	switch serviceOf(norm) {
	case "matching", "inventory":
		return nil
	}
	var out []string
	for dep := range deps {
		if strings.HasPrefix(dep, root+"/") {
			out = append(out, fmt.Sprintf("%s прямо или транзитивно импортирует %s: код с ключом номера случая разрешён только matching и inventory (ADR-0020)", pkg, dep))
		}
	}
	return out
}

// closure — транзитивное замыкание импортов start (без самого start); циклы не мешают обходу.
func closure(start string, next func(string) []string) map[string]bool {
	seen := map[string]bool{}
	stack := append([]string(nil), next(start)...)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[n] || n == start {
			continue
		}
		seen[n] = true
		stack = append(stack, next(n)...)
	}
	return seen
}

// TestK1SealDependencies применяет правило ADR-0020 к реальному дереву пакетов.
func TestK1SealDependencies(t *testing.T) {
	pkgs := loadAll(t)
	byID := map[string]*packages.Package{}
	packages.Visit(pkgs, nil, func(p *packages.Package) { byID[p.ID] = p })
	next := func(id string) []string {
		p := byID[id]
		if p == nil {
			return nil
		}
		ids := make([]string, 0, len(p.Imports))
		for _, imp := range p.Imports {
			ids = append(ids, imp.ID)
		}
		return ids
	}
	for _, p := range pkgs {
		if !strings.HasPrefix(p.PkgPath, module) {
			continue
		}
		deps := map[string]bool{}
		for id := range closure(p.ID, next) {
			if d := byID[id]; d != nil {
				deps[d.PkgPath] = true
			}
		}
		for _, msg := range k1sealViolations(p.PkgPath, deps) {
			t.Error(msg)
		}
	}
}

func TestDependencyRules(t *testing.T) {
	pkgs := loadAll(t)
	kafkaAllowed := func(path string) bool {
		return strings.HasPrefix(path, module+"/libs/messaging") ||
			strings.HasPrefix(path, module+"/libs/testinfra") ||
			strings.HasPrefix(path, module+"/tools/") ||
			strings.HasPrefix(path, module+"/tests/")
	}
	for _, p := range pkgs {
		if !strings.HasPrefix(p.PkgPath, module) {
			continue
		}
		own := serviceOf(p.PkgPath)
		isLib := strings.HasPrefix(p.PkgPath, module+"/libs/")
		isDomain := strings.Contains(p.PkgPath, "/internal/domain")
		for imp := range p.Imports {
			if isLib && strings.HasPrefix(imp, module+"/services/") {
				t.Errorf("%s (libs) импортирует сервис %s", p.PkgPath, imp)
			}
			if own != "" {
				if other := serviceOf(imp); other != "" && other != own {
					t.Errorf("сервис %s импортирует сервис %s (%s → %s)", own, other, p.PkgPath, imp)
				}
			}
			if isDomain {
				for _, banned := range []string{"github.com/jackc/pgx", "github.com/twmb/franz-go", "net/http", "database/sql", module + "/libs/messaging", module + "/libs/pgx"} {
					if strings.HasPrefix(imp, banned) {
						t.Errorf("домен %s зависит от инфраструктуры %s", p.PkgPath, imp)
					}
				}
			}
			if strings.HasPrefix(imp, "github.com/twmb/franz-go") && !kafkaAllowed(p.PkgPath) {
				t.Errorf("%s импортирует Kafka напрямую; разрешено только через libs/messaging", p.PkgPath)
			}
		}
	}
}
