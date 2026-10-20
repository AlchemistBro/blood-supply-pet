package observability

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/correlation"
)

func TestMaskingAndCorrelation(t *testing.T) {
	var buf bytes.Buffer
	h := &contextHandler{Handler: slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: maskAttr})}
	log := slog.New(h)
	ctx := correlation.With(context.Background(), correlation.IDs{Correlation: "corr-1"})
	log.InfoContext(ctx, "заявка", "patient_case_no", "CASE-42", "phenotype", "Kell-", "clinic_id", 7)
	out := buf.String()
	if strings.Contains(out, "CASE-42") || strings.Contains(out, "Kell-") {
		t.Fatalf("чувствительные значения попали в лог: %s", out)
	}
	if !strings.Contains(out, `"patient_case_no":"[скрыто]"`) || !strings.Contains(out, `"clinic_id":7`) {
		t.Fatalf("маскирование сработало неверно: %s", out)
	}
	if !strings.Contains(out, `"correlation_id":"corr-1"`) {
		t.Fatalf("correlation_id не добавлен: %s", out)
	}
}

func TestInitWithoutCollector(t *testing.T) {
	o, err := Init(context.Background(), Config{ServiceName: "test", Version: "dev", LogFormat: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if o.MetricsHandler == nil || o.Tracer == nil || o.Logger == nil {
		t.Fatal("провайдеры не собраны")
	}
	if err := o.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
