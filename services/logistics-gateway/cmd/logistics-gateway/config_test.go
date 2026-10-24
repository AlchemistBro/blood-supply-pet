package main

import (
	"testing"
	"time"
)

func validConfig() config {
	return config{
		C03URL: "http://c03", C03Timeout: 5 * time.Second, Lease: 30 * time.Second, MaxAttempts: 5,
		Backoff: time.Second, UnknownRecheck: time.Minute, ReconcileInterval: time.Second, ReconcileBatch: 100,
	}
}

func TestDefaultsMatchPrototype(t *testing.T) {
	t.Setenv("BS_C03_URL", "http://c03")
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	s := c.settings()
	if s.Lease != 30*time.Second || s.Retry.MaxAttempts != 5 || s.Retry.Backoff != time.Second ||
		s.Retry.UnknownRecheck != time.Minute || s.ReconcileInterval != time.Second || s.ReconcileBatch != 100 ||
		c.C03Timeout != 5*time.Second || !c.ReconcilerEnabled {
		t.Fatalf("значения по умолчанию расходятся с прототипом: %+v, %+v", c, s)
	}
}

// Аренда обязана пережить все вызовы C03 одного drive, иначе исход запишет уже не владелец.
func TestLeaseMustOutliveCarrierCalls(t *testing.T) {
	c := validConfig()
	c.C03Timeout = 10 * time.Second
	if err := c.validate(); err == nil {
		t.Fatal("таймаут C03 10 с × 3 при аренде 30 с должен быть отвергнут")
	}
	if err := validConfig().validate(); err != nil {
		t.Fatalf("корректная конфигурация отвергнута: %v", err)
	}
}

func TestInvalidConfigIsRejected(t *testing.T) {
	cases := map[string]func(c *config){
		"нет адреса C03":   func(c *config) { c.C03URL = "" },
		"ноль попыток":     func(c *config) { c.MaxAttempts = 0 },
		"нулевая пауза":    func(c *config) { c.Backoff = 0 },
		"пустая пачка":     func(c *config) { c.ReconcileBatch = 0 },
		"нулевой период":   func(c *config) { c.ReconcileInterval = 0 },
		"нулевая аренда":   func(c *config) { c.Lease = 0 },
		"нет перепроверки": func(c *config) { c.UnknownRecheck = 0 },
	}
	for name, mutate := range cases {
		c := validConfig()
		mutate(&c)
		if err := c.validate(); err == nil {
			t.Errorf("%s: конфигурация принята", name)
		}
	}
}
