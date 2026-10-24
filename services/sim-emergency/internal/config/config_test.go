package config

import (
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		ok   bool
	}{
		{"по умолчанию", map[string]string{}, true},
		{"через nginx с префиксом", map[string]string{"BS_PLANNING_URL": "https://localhost/api", "BS_SIM_NO_AUTH": "true"}, true},
		{"без схемы", map[string]string{"BS_PLANNING_URL": "planning:8103"}, false},
		{"нулевой таймаут", map[string]string{"BS_SIM_SEND_TIMEOUT": "0s"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			cfg, err := Load()
			if (err == nil) != tc.ok {
				t.Fatalf("Load() = %+v, %v", cfg, err)
			}
			if tc.ok && tc.name == "по умолчанию" {
				if cfg.HTTPAddr != ":8202" || cfg.PlanningURL != "http://planning:8103" || cfg.NoAuth || cfg.SendTimeout != 10*time.Second {
					t.Fatalf("значения по умолчанию: %+v", cfg)
				}
			}
		})
	}
}
