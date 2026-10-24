package archtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Keycloak 26 выдаёт sub в access token через basic; profile/email проекту не нужны.
func TestKeycloakWebHasIdentityWithoutPersonalScopes(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"deploy/compose/keycloak/realm-blood-supply.json", "deploy/k8s/base/platform/keycloak/realm-blood-supply.json"} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, file))
			if err != nil {
				t.Fatal(err)
			}
			var realm struct {
				Clients []struct {
					ID       string   `json:"clientId"`
					Default  []string `json:"defaultClientScopes"`
					Optional []string `json:"optionalClientScopes"`
					Mappers  []struct {
						Config map[string]string `json:"config"`
					} `json:"protocolMappers"`
				} `json:"clients"`
			}
			if err := json.Unmarshal(raw, &realm); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, client := range realm.Clients {
				if client.ID != "web" {
					continue
				}
				found = true
				if !slices.Contains(client.Default, "basic") {
					t.Error("нет basic: access token останется без sub")
				}
				for _, scope := range []string{"profile", "email", "phone", "address"} {
					if slices.Contains(client.Default, scope) || slices.Contains(client.Optional, scope) {
						t.Errorf("лишний персональный scope: %s", scope)
					}
				}
				username := false
				for _, m := range client.Mappers {
					if m.Config["claim.name"] == "preferred_username" && m.Config["access.token.claim"] == "true" {
						username = true
					}
				}
				if !username {
					t.Error("нет preferred_username в access token")
				}
			}
			if !found {
				t.Fatal("нет клиента web")
			}
		})
	}
}
