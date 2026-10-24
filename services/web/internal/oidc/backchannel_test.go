package oidc

import "testing"

func TestBackchannelUsesInternalRealmAndKeepsPublicIssuer(t *testing.T) {
	cfg := Config{Issuer: "https://public.example/realms/blood", DiscoveryURL: "http://keycloak:8080/realms/blood"}
	for raw, want := range map[string]string{
		"https://public.example/realms/blood/protocol/openid-connect/token": "http://keycloak:8080/realms/blood/protocol/openid-connect/token",
		"https://public.example/realms/blood/protocol/openid-connect/certs": "http://keycloak:8080/realms/blood/protocol/openid-connect/certs",
		"https://public.example/realms/blood-other/token":                   "https://public.example/realms/blood-other/token",
		"https://other.example/token":                                       "https://other.example/token",
	} {
		if got := backchannelURL(raw, cfg); got != want {
			t.Errorf("%s: %s, ожидали %s", raw, got, want)
		}
	}
}
