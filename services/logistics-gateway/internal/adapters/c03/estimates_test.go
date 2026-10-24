package c03_test

import (
	"context"
	"io"
	"net/http"
	"testing"
)

func TestEstimatesTranslateCamelCaseAndRejectInvalidRoutes(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"sourceClinicId":1,"estimatedAt":"2026-10-24T10:00:00Z","estimates":[{"targetClinicId":2,"minutes":15}]}`, true},
		{`{"sourceClinicId":2,"estimatedAt":"2026-10-24T10:00:00Z","estimates":[]}`, false},
		{`{"sourceClinicId":1,"estimatedAt":"2026-10-24T10:00:00Z","estimates":[{"targetClinicId":2,"minutes":-1}]}`, false},
	} {
		client, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("sourceClinicId") != "1" {
				t.Error("нет источника")
			}
			_, _ = io.WriteString(w, tc.body)
		})
		out, err := client.Estimates(context.Background(), 1)
		if (err == nil) != tc.valid {
			t.Fatalf("разбор: %+v %v", out, err)
		}
		if tc.valid && (len(out.Estimates) != 1 || out.Estimates[0].Minutes != 15) {
			t.Fatalf("маршрут потерян: %+v", out)
		}
	}
}
