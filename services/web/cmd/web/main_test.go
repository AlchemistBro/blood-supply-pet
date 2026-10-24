package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/runtime"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/auditclient"
)

func rtFor(env string) *runtime.Runtime {
	return &runtime.Runtime{
		Cfg:   runtime.Config{Environment: env, OIDCIssuer: "http://localhost:8180/realms/blood-supply"},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock: clock.System{},
	}
}

// Без адреса аудита web запускается только локально; в остальных окружениях — отказ старта.
func TestAuditRecorderRequiresAuditOutsideLocal(t *testing.T) {
	if _, err := auditRecorder(config{}, rtFor("k8s")); err == nil {
		t.Fatal("без BS_AUDIT_URL вне local запуск должен останавливаться")
	}
	rec, err := auditRecorder(config{}, rtFor("local"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.(auditclient.LogOnly); !ok {
		t.Fatalf("локально без аудита — запись в лог, получено %T", rec)
	}
	if _, err := auditRecorder(config{AuditURL: "http://audit:8080"}, rtFor("k8s")); err == nil {
		t.Fatal("без секрета служебного клиента аудит недоступен")
	}
	rec, err = auditRecorder(config{AuditURL: "http://audit:8080", ServiceClientID: "services", ServiceClientSecret: "s"}, rtFor("k8s"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.(*auditclient.Client); !ok {
		t.Fatalf("с адресом и секретом — HTTP-клиент аудита, получено %T", rec)
	}
}
