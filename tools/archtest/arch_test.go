// Package archtest проверяет правила зависимостей репозитория (CONTRIBUTING.md, «Архитектурные правила»):
//   - libs/** не импортирует services/**;
//   - services/<a>/** не импортирует services/<b>/** (строже, чем правило internal компилятора);
//   - internal/domain не зависит от pgx, Kafka, HTTP и SQL;
//   - клиент Kafka (franz-go) импортируется только в libs/messaging, libs/testinfra, tools и tests;
//   - расшифровку номера случая libs/k1seal/opener импортируют только matching, inventory и libs/k1seal (ADR-0020).
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

// k1OpenerViolation возвращает текст нарушения, если importer импортирует расшифровку номера случая
// (libs/k1seal/opener) без права на ключ K1 (ADR-0020). Пустая строка — нарушения нет.
// Внутренний пакет libs/k1seal/internal/... ограничивает сам компилятор.
func k1OpenerViolation(importer, imported string) string {
	if imported != module+"/libs/k1seal/opener" {
		return ""
	}
	// Внешний тестовый пакет (…_test) проверяется как пакет, который он тестирует.
	path := strings.TrimSuffix(importer, "_test")
	switch serviceOf(path) {
	case "matching", "inventory":
		return ""
	}
	if path == module+"/libs/k1seal" || strings.HasPrefix(path, module+"/libs/k1seal/") {
		return ""
	}
	return fmt.Sprintf("%s импортирует расшифровку номера случая %s; разрешено только matching и inventory (ADR-0020)", importer, imported)
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
			if msg := k1OpenerViolation(p.PkgPath, imp); msg != "" {
				t.Error(msg)
			}
		}
	}
}
