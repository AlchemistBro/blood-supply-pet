package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/transfer/api"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData(apispec.Spec)
	if err != nil {
		t.Fatalf("разбор openapi.yaml: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("openapi.yaml не проходит проверку: %v", err)
	}
	return doc
}

// Каждая операция контракта несёт x-bs-operation, и оно совпадает с матрицей прав маршрутизатора.
func TestEveryOperationDeclaresPolicyOperation(t *testing.T) {
	doc := loadSpec(t)
	known := map[auth.Operation]bool{}
	for _, op := range auth.Operations() {
		known[op] = true
	}
	seen := 0
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			pattern := method + " " + path
			raw, ok := op.Extensions["x-bs-operation"]
			if !ok {
				t.Errorf("%s: нет x-bs-operation", pattern)
				continue
			}
			name, _ := raw.(string)
			if !known[auth.Operation(name)] {
				t.Errorf("%s: x-bs-operation %q нет в policy.go", pattern, name)
			}
			if got, ok := api.Operations[pattern]; !ok || string(got) != name {
				t.Errorf("%s: маршрутизатор проверяет %q, контракт объявляет %q", pattern, got, name)
			}
			seen++
		}
	}
	if seen != len(api.Operations) {
		t.Errorf("в контракте %d операций, в маршрутизаторе %d", seen, len(api.Operations))
	}
}

// Ответы подтверждения перечисляют все восемь исходов, ответы замены — все шесть.
func TestOutcomeEnumsCoverDomain(t *testing.T) {
	doc := loadSpec(t)
	check := func(schema string, want []string) {
		s := doc.Components.Schemas[schema].Value.Properties["code"].Value
		got := map[string]bool{}
		for _, v := range s.Enum {
			got[v.(string)] = true
		}
		for _, c := range want {
			if !got[c] {
				t.Errorf("%s.code: нет исхода %s", schema, c)
			}
		}
		if len(s.Enum) != len(want) {
			t.Errorf("%s.code: %d значений, ожидали %d", schema, len(s.Enum), len(want))
		}
	}
	check("ConfirmationResult", domain.ConfirmationCodes)
	check("ReplacementResult", domain.ReplacementCodes)
	if doc.Paths.Find("/obligations/{id}/confirm").Post.Responses.Status(http.StatusServiceUnavailable) == nil {
		t.Error("у подтверждения нет ответа 503 (АИСТ-М не ответила)")
	}
}
