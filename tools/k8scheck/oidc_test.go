package main

import (
	"strings"
	"testing"
)

func TestOIDCRoutes(t *testing.T) {
	const manifest = `
kind: Ingress
spec:
  rules:
    - host: localhost
      http:
        paths:
          - path: /auth
            pathType: Prefix
            backend: {service: {name: keycloak}}
---
kind: Ingress
spec:
  rules:
    - host: localhost
      http:
        paths:
          - path: /auth/login
            pathType: Exact
            backend: {service: {name: web}}
          - path: /auth/callback
            pathType: Exact
            backend: {service: {name: web}}
`
	for _, tc := range []struct {
		name, manifest string
		want           int
	}{
		{"маршруты разделены", manifest, 0},
		{"потерян callback", strings.ReplaceAll(manifest, "/auth/callback", "/callback"), 1},
		{"BFF отправлен в Keycloak", strings.ReplaceAll(manifest, "name: web", "name: keycloak"), 2},
		{"нет точных маршрутов", strings.ReplaceAll(manifest, "pathType: Exact", "pathType: Prefix"), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs, err := parse(strings.NewReader(tc.manifest))
			if err != nil {
				t.Fatal(err)
			}
			if got := checkOIDCRoutes(docs); len(got) != tc.want {
				t.Fatalf("замечания: %v; ожидали %d", got, tc.want)
			}
		})
	}
}
