// k8scheck проверяет собранные манифесты Kubernetes (вывод kubectl kustomize) без кластера и без сети:
//   - каждый Deployment с меткой blood-supply.io/runtime=go-service соответствует стандарту сервиса
//     (deploy/k8s/components/go-service): три пробы, ресурсы, runAsNonRoot, корень ФС только для чтения, /tmp;
//   - ссылки env/envFrom ведут на существующие ConfigMap и Secret того же namespace и на существующие ключи
//     (кроме optional), переменные не повторяются.
//
// Запуск: kubectl kustomize deploy/k8s/overlays/local | go run ./tools/k8scheck
// или go run ./tools/k8scheck <файл.yaml>. Код выхода 1 — есть замечания.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type doc = map[string]any

func main() {
	in := io.Reader(os.Stdin)
	if len(os.Args) > 1 {
		f, err := os.Open(os.Args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "k8scheck:", err)
			os.Exit(2)
		}
		defer func() { _ = f.Close() }()
		in = f
	}
	docs, err := parse(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "k8scheck:", err)
		os.Exit(2)
	}
	problems := check(docs)
	if len(problems) == 0 {
		fmt.Printf("k8scheck: объектов %d, замечаний нет\n", len(docs))
		return
	}
	fmt.Printf("k8scheck: замечаний %d\n", len(problems))
	for _, p := range problems {
		fmt.Println("  -", p)
	}
	os.Exit(1)
}

// parse читает поток YAML-документов, пустые документы пропускает.
func parse(r io.Reader) ([]doc, error) {
	dec := yaml.NewDecoder(r)
	var docs []doc
	for {
		var d doc
		if err := dec.Decode(&d); err != nil {
			if errors.Is(err, io.EOF) {
				return docs, nil
			}
			return nil, fmt.Errorf("разбор YAML: %w", err)
		}
		if d != nil {
			docs = append(docs, d)
		}
	}
}

// check возвращает замечания в устойчивом порядке.
func check(docs []doc) []string {
	idx := index(docs)
	var problems []string
	for _, d := range docs {
		if str(d, "kind") != "Deployment" || str(d, "metadata", "labels", "blood-supply.io/runtime") != "go-service" {
			continue
		}
		problems = append(problems, checkService(d, idx)...)
	}
	sort.Strings(problems)
	return problems
}

// index — ConfigMap и Secret по ключу «вид/namespace/имя» с набором их ключей.
func index(docs []doc) map[string]map[string]bool {
	idx := map[string]map[string]bool{}
	for _, d := range docs {
		kind := str(d, "kind")
		if kind != "ConfigMap" && kind != "Secret" {
			continue
		}
		keys := map[string]bool{}
		for _, field := range []string{"data", "stringData", "binaryData"} {
			if m, ok := get(d, field).(doc); ok {
				for k := range m {
					keys[k] = true
				}
			}
		}
		idx[kind+"/"+str(d, "metadata", "namespace")+"/"+str(d, "metadata", "name")] = keys
	}
	return idx
}

func checkService(d doc, idx map[string]map[string]bool) []string {
	ns, name := str(d, "metadata", "namespace"), str(d, "metadata", "name")
	where := ns + "/" + name
	spec := get(d, "spec", "template", "spec")
	var out []string
	add := func(format string, args ...any) { out = append(out, where+": "+fmt.Sprintf(format, args...)) }

	if get(spec, "securityContext", "runAsNonRoot") != true {
		add("нет runAsNonRoot: true у пода")
	}
	if len(list(spec, "volumes")) == 0 {
		add("нет тома для /tmp (корень ФС только для чтения)")
	}
	containers := list(spec, "containers")
	if len(containers) != 1 {
		add("контейнеров %d, ожидается один (app)", len(containers))
		return out
	}
	c := containers[0]
	for _, field := range []string{"image", "startupProbe", "readinessProbe", "livenessProbe", "resources"} {
		if get(c, field) == nil {
			add("нет %s", field)
		}
	}
	if get(c, "securityContext", "readOnlyRootFilesystem") != true {
		add("нет readOnlyRootFilesystem: true")
	}

	seen := map[string]bool{}
	for _, e := range list(c, "env") {
		envName := str(e, "name")
		if seen[envName] {
			add("повтор переменной %s", envName)
		}
		seen[envName] = true
		for kind, refField := range map[string]string{"ConfigMap": "configMapKeyRef", "Secret": "secretKeyRef"} {
			ref := get(e, "valueFrom", refField)
			if ref == nil {
				continue
			}
			refName, key := str(ref, "name"), str(ref, "key")
			keys, ok := idx[kind+"/"+ns+"/"+refName]
			switch {
			case !ok:
				add("%s: %s %s не найден в %s", envName, kind, refName, ns)
			case !keys[key] && get(ref, "optional") != true:
				add("%s: в %s %s нет ключа %s", envName, kind, refName, key)
			}
		}
	}
	for _, ef := range list(c, "envFrom") {
		for kind, refField := range map[string]string{"ConfigMap": "configMapRef", "Secret": "secretRef"} {
			ref := get(ef, refField)
			if ref == nil || get(ref, "optional") == true {
				continue
			}
			if _, ok := idx[kind+"/"+ns+"/"+str(ref, "name")]; !ok {
				add("envFrom: %s %s не найден в %s", kind, str(ref, "name"), ns)
			}
		}
	}
	return out
}

func get(v any, path ...string) any {
	for _, p := range path {
		m, ok := v.(doc)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

func str(v any, path ...string) string {
	s, _ := get(v, path...).(string)
	return strings.TrimSpace(s)
}

func list(v any, path ...string) []any {
	l, _ := get(v, path...).([]any)
	return l
}
