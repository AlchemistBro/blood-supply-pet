package archtest

import "testing"

// Правило ADR-0020 на фальшивом дереве импортов. Код с ключом (libs/k1seal/opener и любой подпакет libs/k1seal)
// достижим, прямо или транзитивно, только из matching, inventory и самой libs/k1seal; тип конверта libs/k1seal
// импортирует кто угодно; список подпакетов libs/k1seal закрыт.
func TestK1SealRule(t *testing.T) {
	const (
		root   = module + "/libs/k1seal"
		opener = module + "/libs/k1seal/opener"
	)
	deps := func(paths ...string) map[string]bool {
		m := map[string]bool{}
		for _, p := range paths {
			m[p] = true
		}
		return m
	}
	cases := []struct {
		name    string
		pkg     string
		deps    map[string]bool
		allowed bool
	}{
		{"matching напрямую", module + "/services/matching/internal/app", deps(opener, root), true},
		{"matching транзитивно", module + "/services/matching/cmd/matching", deps(module+"/services/matching/internal/app", opener), true},
		{"inventory напрямую", module + "/services/inventory/internal/adapters/aist", deps(opener), true},
		{"внешний тест inventory", module + "/services/inventory/internal/app_test", deps(opener), true},
		{"сам корень", root, deps(), true},
		{"внешний тест корня", root + "_test", deps(opener, root), true},
		{"opener", opener, deps(root), true},
		{"внешний тест opener", opener + "_test", deps(opener, root), true},
		{"тестовый main opener", opener + ".test", deps(opener, root), true},

		{"transfer носит конверт", module + "/services/transfer/internal/app", deps(root, module+"/libs/contracts"), true},
		{"контракты носят конверт", module + "/libs/contracts", deps(root), true},
		{"web носит конверт", module + "/services/web/internal/handlers", deps(root), true},

		{"transfer напрямую", module + "/services/transfer/internal/app", deps(opener, root), false},
		{"transfer транзитивно через библиотеку", module + "/services/transfer/internal/app", deps(module+"/libs/patientcase", opener, root), false},
		{"библиотека-обёртка над opener", module + "/libs/patientcase", deps(opener, root), false},
		{"web транзитивно", module + "/services/web/cmd/web", deps(module+"/services/web/internal/handlers", opener), false},
		{"audit", module + "/services/audit/internal/app", deps(opener), false},
		{"похожее имя сервиса", module + "/services/matching-sim/internal/app", deps(opener), false},
		{"похожее имя сервиса 2", module + "/services/inventoryx", deps(opener), false},
		{"похожее имя библиотеки", module + "/libs/k1sealx", deps(opener), false},
		{"tools", module + "/tools/auditverify", deps(opener), false},
		{"e2e", module + "/tests/e2e", deps(opener), false},
		{"чужой подпакет k1seal транзитивно", module + "/services/web/internal/handlers", deps(root + "/internal/envelope"), false},
		{"новый подпакет k1seal", root + "/extra", deps(root), false},
		{"новый внутренний подпакет k1seal", root + "/internal/envelope", deps(root), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := k1sealViolations(c.pkg, c.deps)
			if c.allowed && len(v) > 0 {
				t.Errorf("%s должно быть разрешено, получено: %v", c.pkg, v)
			}
			if !c.allowed && len(v) == 0 {
				t.Errorf("%s должно быть запрещено", c.pkg)
			}
		})
	}
}

// Транзитивное замыкание импортов на фальшивом графе: путь через промежуточный пакет находится.
func TestTransitiveImports(t *testing.T) {
	graph := map[string][]string{
		"a": {"b"},
		"b": {"c", "d"},
		"c": {"opener"},
		"d": {"b"}, // цикл не зацикливает обход
	}
	got := closure("a", func(p string) []string { return graph[p] })
	for _, want := range []string{"b", "c", "d", "opener"} {
		if !got[want] {
			t.Errorf("в замыкании нет %s: %v", want, got)
		}
	}
	if got["a"] {
		t.Errorf("пакет не должен входить в собственное замыкание: %v", got)
	}
}
