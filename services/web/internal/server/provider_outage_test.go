package server

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestProviderOutageKeepsValidSessionAndExplainsNewLogin(t *testing.T) {
	e := newEnv(t)
	j := e.login(operator, "/tasks")
	e.provider.SetUnavailable(true)
	if response := e.do(http.MethodGet, "/tasks", j, nil); response.Code != http.StatusOK || e.gotToken == "" {
		t.Fatalf("действующая сессия не должна зависеть от discovery: %d", response.Code)
	}
	newJar := jar{}
	response := e.do(http.MethodGet, "/auth/login?next=/tasks", newJar, nil)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "Служба входа временно недоступна") || response.Header().Get("Retry-After") == "" {
		t.Fatalf("новый вход должен объяснить отказ провайдера: %d", response.Code)
	}
	if newJar.hasPrefix("bs_auth.") || response.Header().Get("Location") != "" {
		t.Fatal("нельзя перенаправлять в недоступный провайдер или создавать состояние входа")
	}
	oldCookie := j["bs_session.0"]
	e.clock.Advance(4*time.Minute + 45*time.Second)
	response = e.do(http.MethodGet, "/tasks", j, nil)
	if response.Code != http.StatusOK || j["bs_session.0"] != oldCookie {
		t.Fatal("сбой раннего refresh не должен отзывать или продлевать действующий токен")
	}
	e.clock.Advance(time.Minute)
	response = e.do(http.MethodGet, "/tasks", j, nil)
	if response.Code != http.StatusSeeOther || j.hasPrefix("bs_session.") {
		t.Fatal("просроченный токен не должен продлеваться при отказе провайдера")
	}
	e.provider.SetUnavailable(false)
	if response := e.do(http.MethodGet, "/auth/login", newJar, nil); response.Code != http.StatusFound {
		t.Fatalf("восстановленный провайдер не принимает новый вход: %d", response.Code)
	}
}
