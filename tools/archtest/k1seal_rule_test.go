package archtest

import "testing"

// Правило ADR-0020 на фальшивом дереве импортов: расшифровку номера случая (libs/k1seal/opener)
// импортируют только matching, inventory и сама libs/k1seal; тип конверта (libs/k1seal) — кто угодно.
func TestK1OpenerRule(t *testing.T) {
	const (
		opener = module + "/libs/k1seal/opener"
		sealed = module + "/libs/k1seal"
	)
	cases := []struct {
		importer, imported string
		allowed            bool
	}{
		{module + "/services/matching/internal/app", opener, true},
		{module + "/services/matching/internal/adapters/http", opener, true},
		{module + "/services/matching/cmd/matching", opener, true},
		{module + "/services/inventory/internal/adapters/aist", opener, true},
		{module + "/services/inventory/internal/app_test", opener, true},
		{module + "/libs/k1seal", opener, true},
		{module + "/libs/k1seal_test", opener, true},
		{module + "/libs/k1seal/opener_test", opener, true},

		{module + "/services/transfer/internal/app", opener, false},
		{module + "/services/web/internal/handlers", opener, false},
		{module + "/services/audit/internal/app", opener, false},
		{module + "/services/matching-sim/internal/app", opener, false},
		{module + "/services/inventoryx", opener, false},
		{module + "/libs/contracts", opener, false},
		{module + "/libs/k1sealx", opener, false},
		{module + "/tools/auditverify", opener, false},
		{module + "/tests/e2e", opener, false},

		// Тип конверта и шифрование доступны всем: transfer и контракты носят конверт.
		{module + "/services/transfer/internal/app", sealed, true},
		{module + "/libs/contracts", sealed, true},
		{module + "/services/web/internal/handlers", sealed, true},
	}
	for _, c := range cases {
		msg := k1OpenerViolation(c.importer, c.imported)
		if c.allowed && msg != "" {
			t.Errorf("%s → %s должно быть разрешено, получено: %s", c.importer, c.imported, msg)
		}
		if !c.allowed && msg == "" {
			t.Errorf("%s → %s должно быть запрещено", c.importer, c.imported)
		}
	}
}
