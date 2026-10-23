// Команда authmatrix проверяет соглашение x-bs-operation / x-bs-public в контрактах сервисов и строит
// таблицу прав «роль × эндпоинт» (NFR011, docs/architecture/testing.md, раздел «Матрица прав»).
//
//	go run ./tools/authmatrix -check                                  # проверка (task authmatrix, CI)
//	go run ./tools/authmatrix -out docs/architecture/auth-matrix.md    # перегенерировать таблицу
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AlchemistBro/blood-supply-pet/tools/authmatrix/matrix"
)

func main() {
	os.Exit(run())
}

func run() int {
	rootFlag := flag.String("root", ".", "каталог внутри репозитория; корень ищется вверх до go.mod")
	out := flag.String("out", "", "записать таблицу в файл (обычно "+matrix.DocFile+")")
	check := flag.Bool("check", false, "проверить контракты и актуальность "+matrix.DocFile)
	flag.Parse()

	root, err := matrix.FindRepoRoot(*rootFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		return 1
	}
	eps, err := matrix.LoadRepo(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "контракты не соблюдают соглашение о матрице прав:")
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := matrix.CheckRoles(filepath.Join(root, filepath.FromSlash(matrix.PolicyFile))); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		return 1
	}
	md := matrix.Markdown(eps)

	if *out != "" {
		if err := os.WriteFile(*out, md, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "ошибка записи:", err)
			return 1
		}
		fmt.Println("записан", *out)
	}
	if *check {
		doc := filepath.Join(root, filepath.FromSlash(matrix.DocFile))
		current, err := os.ReadFile(filepath.Clean(doc))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ошибка:", err)
			return 1
		}
		if !bytes.Equal(bytes.ReplaceAll(current, []byte("\r\n"), []byte("\n")), md) {
			fmt.Fprintf(os.Stderr, "%s устарел: выполните go run ./tools/authmatrix -out %s и закоммитьте\n", matrix.DocFile, matrix.DocFile)
			return 1
		}
	}
	if *out == "" && !*check {
		_, _ = os.Stdout.Write(md)
		return 0
	}

	protected, public := 0, 0
	for _, e := range eps {
		if e.Public {
			public++
		} else {
			protected++
		}
	}
	fmt.Printf("матрица прав согласована: защищённых операций %d, публичных %d, ролей %d\n", protected, public, len(matrix.Roles()))
	return 0
}
