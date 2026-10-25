package main

// Keycloak и BFF делят /auth на одном хосте. Без отдельных маршрутов BFF
// начало входа и callback попадают в Keycloak и браузер получает 404.
func checkOIDCRoutes(docs []doc) []string {
	keycloakHosts := map[string]bool{}
	webRoutes := map[string]map[string]bool{}
	for _, d := range docs {
		if str(d, "kind") != "Ingress" {
			continue
		}
		for _, rawRule := range list(d, "spec", "rules") {
			rule, _ := rawRule.(map[string]any)
			host := str(rule, "host")
			for _, rawPath := range list(rule, "http", "paths") {
				path, _ := rawPath.(map[string]any)
				name := str(path, "backend", "service", "name")
				if name == "keycloak" && str(path, "path") == "/auth" {
					keycloakHosts[host] = true
				}
				if name == "web" && str(path, "pathType") == "Exact" {
					if webRoutes[host] == nil {
						webRoutes[host] = map[string]bool{}
					}
					webRoutes[host][str(path, "path")] = true
				}
			}
		}
	}
	var problems []string
	for host := range keycloakHosts {
		for _, path := range []string{"/auth/login", "/auth/callback"} {
			if !webRoutes[host][path] {
				problems = append(problems, "Ingress "+host+": нужен Exact-маршрут "+path+" в web поверх /auth Keycloak")
			}
		}
	}
	return problems
}
