package config_test

import (
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/config"
)

func TestDefaults(t *testing.T) {
	t.Setenv("BS_AIST_URL", "http://sim-aist:8201")
	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.PollInterval != 5*time.Second || c.StatusTimeout != 3*time.Second || c.PageLimit != 500 {
		t.Fatalf("значения по умолчанию: %+v", c)
	}
}

func TestRejectsBadValues(t *testing.T) {
	cases := map[string][2]string{
		"нулевой интервал":     {"BS_AIST_POLL_INTERVAL", "0s"},
		"страница больше 5000": {"BS_AIST_PAGE_LIMIT", "5001"},
		"нулевой таймаут":      {"BS_AIST_STATUS_TIMEOUT", "0s"},
	}
	for name, kv := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(kv[0], kv[1])
			if _, err := config.Load(); err == nil {
				t.Fatal("ожидали ошибку конфигурации")
			}
		})
	}
}
