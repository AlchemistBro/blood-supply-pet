package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
)

// demandStub отвечает как сервис demand: пустой список файлов и заданный результат проверки загруженного CSV.
func demandStub(t *testing.T, status string) seeder {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"file":{"id":"11111111-1111-1111-1111-111111111111","check_status":"` + status + `"}}`))
	}))
	t.Cleanup(srv.Close)
	return seeder{ctx: context.Background(), http: &http.Client{Timeout: 5 * time.Second}, demand: srv.URL}
}

// Часть строк истории расхода может быть отклонена как пересечение с уже загруженными данными за те же периоды:
// оценка берёт всю существующую историю прошлого года, поэтому демо-наполнение продолжается.
func TestHistoryPartialIsAcceptedBecauseEstimateUsesExistingHistory(t *testing.T) {
	s := demandStub(t, "PARTIAL")
	id, err := s.file("token", "CONSUMPTION_HISTORY", testdata.CSVFile{Name: "h.csv", Content: []byte("a;b\n")})
	if err != nil {
		t.Fatalf("PARTIAL истории должен приниматься: %v", err)
	}
	if id.String() != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("неверный идентификатор файла: %s", id)
	}
}

// Для профиля районов и долей частичный приём недопустим: расчёт построится на неполных входах.
func TestOtherKindsPartialStillFails(t *testing.T) {
	for _, kind := range []string{"DISTRICT_PROFILE", "CLINIC_SHARE"} {
		s := demandStub(t, "PARTIAL")
		_, err := s.file("token", kind, testdata.CSVFile{Name: "x.csv", Content: []byte("a;b\n")})
		if err == nil || !strings.Contains(err.Error(), "не принят целиком") {
			t.Fatalf("%s: ожидалась ошибка «не принят целиком», получено %v", kind, err)
		}
	}
}

func TestRejectedHistoryStillFails(t *testing.T) {
	s := demandStub(t, "REJECTED")
	if _, err := s.file("token", "CONSUMPTION_HISTORY", testdata.CSVFile{Name: "h.csv", Content: []byte("a;b\n")}); err == nil {
		t.Fatal("полностью отклонённая история не должна приниматься")
	}
}
