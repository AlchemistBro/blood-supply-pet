// Команда scaffold создаёт заготовку сервиса: go run ./tools/scaffold/cmd --name matching --port 8104
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/AlchemistBro/blood-supply-pet/tools/scaffold"
)

func main() {
	os.Exit(run())
}

func run() int {
	name := flag.String("name", "", "имя сервиса (строчные буквы, цифры, дефис)")
	module := flag.String("module", "", "модуль аудита; по умолчанию имя без дефисов")
	db := flag.String("db", "", "имя базы и роли; по умолчанию имя с подчёркиваниями")
	port := flag.Int("port", 0, "порт сервиса в compose")
	modulePath := flag.String("module-path", "github.com/AlchemistBro/blood-supply-pet", "путь модуля Go")
	root := flag.String("root", ".", "корень репозитория")
	flag.Parse()

	p := scaffold.Params{Name: *name, Module: *module, DBName: *db, Port: *port, ModulePath: *modulePath}
	written, err := scaffold.Write(*root, p)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		return 1
	}
	for _, w := range written {
		fmt.Println("создан", w)
	}
	if added, err := scaffold.RegisterInTaskfile(*root, p.Name); err != nil {
		fmt.Fprintln(os.Stderr, "Taskfile не обновлён:", err)
	} else if added {
		fmt.Println("сервис добавлен в SERVICES в Taskfile.yml")
	}
	fmt.Printf(`
Дальше:
  1. Описать контракт в services/%[1]s/api/openapi.yaml и выполнить task gen.
  2. Добавить роль и базу в deploy/compose/postgres/init.sql и сервис в deploy/compose/compose.yaml (порт %[2]d).
  3. Написать тест, затем реализацию (CONTRIBUTING.md, рабочий цикл).
`, p.Name, p.Port)
	return 0
}
