package scaffold_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/tools/scaffold"
)

func TestRenderProducesValidGoAndSubstitutes(t *testing.T) {
	files, err := scaffold.Render(scaffold.Params{Name: "demo-svc", ModulePath: "example.com/m", Port: 8190})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Path] = string(f.Content)
	}
	main := got["services/demo-svc/cmd/demo-svc/main.go"]
	for _, want := range []string{`Name:       "demo-svc"`, `Module:     "demosvc"`, `example.com/m/libs/runtime`} {
		if !strings.Contains(main, want) {
			t.Errorf("main.go не содержит %q:\n%s", want, main)
		}
	}
	if !strings.Contains(got["services/demo-svc/api/openapi.yaml"], "http://localhost:8190") {
		t.Error("порт не подставлен в openapi.yaml")
	}
	if !strings.Contains(got["services/demo-svc/migrations/00100_init.sql"], "база demo_svc") {
		t.Error("имя базы не подставлено в миграцию")
	}
	for path := range got {
		if strings.Contains(got[path], "[[") {
			t.Errorf("%s: остались неподставленные поля шаблона", path)
		}
	}
}

func TestValidateRejectsBadParams(t *testing.T) {
	cases := map[string]scaffold.Params{
		"заглавные буквы": {Name: "Demo", ModulePath: "m", Port: 8190},
		"пустое имя":      {Name: "", ModulePath: "m", Port: 8190},
		"путь вверх":      {Name: "../x", ModulePath: "m", Port: 8190},
		"низкий порт":     {Name: "demo", ModulePath: "m", Port: 80},
		"нет модуля":      {Name: "demo", Port: 8190},
	}
	for name, p := range cases {
		if _, err := scaffold.Render(p); err == nil {
			t.Errorf("%s: ожидали ошибку", name)
		}
	}
}

func TestWriteRefusesToOverwriteAndRegistersInTaskfile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Taskfile.yml"), []byte("vars:\n  SERVICES: audit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := scaffold.Params{Name: "demo", ModulePath: "example.com/m", Port: 8190}
	if _, err := scaffold.Write(root, p); err != nil {
		t.Fatal(err)
	}
	if _, err := scaffold.Write(root, p); err == nil {
		t.Fatal("повторная генерация должна отказывать, а не перезаписывать")
	}
	added, err := scaffold.RegisterInTaskfile(root, "demo")
	if err != nil || !added {
		t.Fatalf("регистрация: added=%v err=%v", added, err)
	}
	again, err := scaffold.RegisterInTaskfile(root, "demo")
	if err != nil || again {
		t.Fatalf("повторная регистрация должна быть no-op: added=%v err=%v", again, err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if !strings.Contains(string(b), "SERVICES: audit demo") {
		t.Fatalf("Taskfile: %q", b)
	}
}

// Заготовка должна компилироваться в составе настоящего репозитория: проверяем go vet на сгенерированном сервисе.
func TestGeneratedServiceCompilesInRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("пропуск в коротком режиме")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	p := scaffold.Params{Name: "scaffoldprobe", ModulePath: "github.com/AlchemistBro/blood-supply-pet", Port: 8199}
	if _, err := scaffold.Write(root, p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(root, "services", p.Name)) })
	cmd := exec.Command("go", "vet", "./services/scaffoldprobe/...")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go vet сгенерированного сервиса: %v\n%s", err, out)
	}
}
