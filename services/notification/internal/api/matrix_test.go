package api_test

import (
	"net/http"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/notification/api"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/tools/authmatrix/matrixtest"
)

func TestOpenAPIRejectsEveryUnauthorizedRole(t *testing.T) {
	matrixtest.CheckDenials(t, "notification", apispec.Spec, func(mux *http.ServeMux, denied auth.Denied) {
		api.Mount(mux, nil, denied)
	})
}
