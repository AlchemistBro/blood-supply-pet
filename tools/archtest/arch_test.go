// Package archtest проверяет правила зависимостей репозитория (CONTRIBUTING.md, «Архитектурные правила»):
//   - libs/** не импортирует services/**;
//   - services/<a>/** не импортирует services/<b>/** (строже, чем правило internal компилятора);
//   - internal/domain не зависит от pgx, Kafka, HTTP и SQL;
//   - клиент Kafka (franz-go) импортируется только в libs/messaging, libs/testinfra, tools и tests.
package archtest

import (
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
