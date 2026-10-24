package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/transfer/api"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/tools/authmatrix/matrixtest"
)

func TestOpenAPIRejectsEveryUnauthorizedRole(t *testing.T) {
	matrixtest.CheckDenials(t, "transfer", apispec.Spec, func(mux *http.ServeMux, denied auth.Denied) {
		api.Mount(mux, nil, refdata.MustLoad(), slog.New(slog.NewTextHandler(io.Discard, nil)), denied)
	})
}
