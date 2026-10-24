package api_test

import (
	"net/http"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/api"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/tools/authmatrix/matrixtest"
)

func TestOpenAPIRejectsEveryUnauthorizedRole(t *testing.T) {
	matrixtest.CheckDenials(t, "logistics-gateway", apispec.Spec, func(mux *http.ServeMux, denied auth.Denied) {
		api.Mount(mux, nil, denied)
	})
}
