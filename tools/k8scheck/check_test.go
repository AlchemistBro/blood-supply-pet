package main

import (
	"strings"
	"testing"
)

// Манифест, который проходит все проверки: сервис по стандарту go-service со ссылками на существующие
// ConfigMap и Secret (в том числе необязательный ключ, которого нет).
const goodManifest = `
apiVersion: v1
kind: ConfigMap
metadata: {name: platform-env-abc, namespace: bs-app}
data: {BS_ENV: local}
---
apiVersion: v1
kind: Secret
metadata: {name: audit-db, namespace: bs-app}
stringData: {BS_DB_URL: "postgres://audit:audit@pg-rw/audit"}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: audit
  namespace: bs-app
  labels: {blood-supply.io/runtime: go-service}
spec:
  template:
    spec:
      securityContext: {runAsNonRoot: true}
      volumes: [{name: tmp, emptyDir: {}}]
      containers:
        - name: app
          image: ghcr.io/alchemistbro/blood-supply-audit:edge
          ports: [{name: http, containerPort: 8108}]
          startupProbe: {httpGet: {path: /healthz, port: http}}
          readinessProbe: {httpGet: {path: /readyz, port: http}}
          livenessProbe: {httpGet: {path: /healthz, port: http}}
          resources: {requests: {cpu: 50m}}
          securityContext: {readOnlyRootFilesystem: true}
          volumeMounts: [{name: tmp, mountPath: /tmp}]
          envFrom: [{configMapRef: {name: platform-env-abc}}]
          env:
            - name: BS_DB_URL
              valueFrom: {secretKeyRef: {name: audit-db, key: BS_DB_URL}}
            - name: BS_OPTIONAL
              valueFrom: {secretKeyRef: {name: audit-db, key: BS_NOPE, optional: true}}
`

func mustParse(t *testing.T, text string) []doc {
	t.Helper()
	docs, err := parse(strings.NewReader(text))
	if err != nil {
		t.Fatalf("разбор YAML: %v", err)
	}
	return docs
}

func TestGoodManifestHasNoProblems(t *testing.T) {
	if problems := check(mustParse(t, goodManifest)); len(problems) != 0 {
		t.Fatalf("ожидались ноль замечаний, получено: %v", problems)
	}
}

// Каждое нарушение стандарта даёт замечание с именем сервиса и сутью.
func TestViolationsAreReported(t *testing.T) {
	cases := map[string]struct{ from, to, want string }{
		"нет readinessProbe":      {"          readinessProbe: {httpGet: {path: /readyz, port: http}}\n", "", "readinessProbe"},
		"корень ФС записываемый":  {"readOnlyRootFilesystem: true", "readOnlyRootFilesystem: false", "readOnlyRootFilesystem"},
		"запуск от root":          {"runAsNonRoot: true", "runAsNonRoot: false", "runAsNonRoot"},
		"нет Secret":              {"{name: audit-db, key: BS_DB_URL}", "{name: missing-db, key: BS_DB_URL}", "missing-db"},
		"нет ключа в Secret":      {"key: BS_DB_URL}}", "key: BS_DB_PASSWORD}}", "BS_DB_PASSWORD"},
		"нет ConfigMap в envFrom": {"configMapRef: {name: platform-env-abc}", "configMapRef: {name: platform-env}", "platform-env"},
		"повтор переменной":       {"- name: BS_OPTIONAL", "- name: BS_DB_URL", "повтор"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			text := strings.Replace(goodManifest, c.from, c.to, 1)
			if text == goodManifest {
				t.Fatalf("замена %q не применилась", c.from)
			}
			problems := check(mustParse(t, text))
			if len(problems) == 0 {
				t.Fatalf("нарушение не найдено")
			}
			joined := strings.Join(problems, "\n")
			if !strings.Contains(joined, "audit") || !strings.Contains(joined, c.want) {
				t.Fatalf("в замечаниях нет %q и имени сервиса: %s", c.want, joined)
			}
		})
	}
}

// Deployment без метки go-service (платформа: Keycloak, MinIO) стандартом сервиса не проверяется.
func TestPlatformDeploymentIsSkipped(t *testing.T) {
	text := strings.Replace(goodManifest, "labels: {blood-supply.io/runtime: go-service}", "labels: {}", 1)
	text = strings.Replace(text, "readOnlyRootFilesystem: true", "readOnlyRootFilesystem: false", 1)
	if problems := check(mustParse(t, text)); len(problems) != 0 {
		t.Fatalf("платформенный Deployment не должен проверяться: %v", problems)
	}
}

const namespaceWithDeny = `
apiVersion: v1
kind: Namespace
metadata: {name: bs-app}
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: default-deny, namespace: bs-app}
spec:
  podSelector: {}
  policyTypes: [Ingress, Egress]
`

// Каждый namespace из сборки закрыт политикой «запрещено всё» на вход и выход.
func TestNamespaceWithDefaultDenyPasses(t *testing.T) {
	if problems := check(mustParse(t, namespaceWithDeny)); len(problems) != 0 {
		t.Fatalf("ожидались ноль замечаний, получено: %v", problems)
	}
}

func TestNamespaceWithoutDefaultDenyIsReported(t *testing.T) {
	cases := map[string]struct{ from, to string }{
		"политики нет":        {"name: default-deny, namespace: bs-app", "name: default-deny, namespace: bs-other"},
		"только вход":         {"policyTypes: [Ingress, Egress]", "policyTypes: [Ingress]"},
		"выбраны не все поды": {"podSelector: {}", "podSelector: {matchLabels: {app: x}}"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			text := strings.Replace(namespaceWithDeny, c.from, c.to, 1)
			if text == namespaceWithDeny {
				t.Fatalf("замена %q не применилась", c.from)
			}
			problems := check(mustParse(t, text))
			if len(problems) != 1 || !strings.Contains(problems[0], "bs-app") || !strings.Contains(problems[0], "default-deny") {
				t.Fatalf("ожидалось одно замечание про bs-app и default-deny, получено: %v", problems)
			}
		})
	}
}
