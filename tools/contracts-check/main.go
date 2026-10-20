// contracts-check проверяет обратную совместимость JSON Schema контрактов с базовой ревизией git:
// обязательные поля нельзя убирать и делать необязательными, типы полей нельзя менять,
// перечисления нельзя сужать. Валидность примеров проверяет go test ./libs/contracts.
//
//	go run ./tools/contracts-check --base origin/main
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type schema struct {
	Required   []string          `json:"required"`
	Properties map[string]schema `json:"properties"`
	Type       any               `json:"type"`
	Enum       []any             `json:"enum"`
}

func main() {
	os.Exit(run())
}

func run() int {
	base := flag.String("base", "origin/main", "ревизия git, с которой сравнивать")
	dir := flag.String("dir", "libs/contracts/schemas", "каталог схем")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	files, err := filepath.Glob(filepath.Join(*dir, "*", "*.json"))
	if err != nil {
		return fail(err)
	}
	problems := 0
	checked := 0
	for _, f := range files {
		rel := filepath.ToSlash(f)
		//nolint:gosec // инструмент разработчика и CI: ревизию задаёт флаг --base, пользовательского ввода нет
		oldRaw, err := exec.CommandContext(ctx, "git", "show", *base+":"+rel).Output()
		if err != nil {
			continue // новая схема: совместимость не с чем сравнивать
		}
		//nolint:gosec // путь получен из filepath.Glob по каталогу схем
		newRaw, err := os.ReadFile(f)
		if err != nil {
			return fail(err)
		}
		var oldS, newS schema
		if err := json.Unmarshal(oldRaw, &oldS); err != nil {
			return fail(fmt.Errorf("%s в %s: %w", rel, *base, err))
		}
		if err := json.Unmarshal(newRaw, &newS); err != nil {
			return fail(fmt.Errorf("%s: %w", rel, err))
		}
		checked++
		for _, p := range compare(rel, "", oldS, newS) {
			fmt.Println("НЕСОВМЕСТИМО:", p)
			problems++
		}
	}
	fmt.Printf("проверено схем: %d (база %s), несовместимых изменений: %d\n", checked, *base, problems)
	if problems > 0 {
		return 1
	}
	return 0
}

func compare(file, path string, oldS, newS schema) []string {
	var out []string
	newRequired := map[string]bool{}
	for _, r := range newS.Required {
		newRequired[r] = true
	}
	for _, r := range oldS.Required {
		if !newRequired[r] {
			out = append(out, fmt.Sprintf("%s: поле %s%s перестало быть обязательным", file, path, r))
		}
	}
	for name, oldProp := range oldS.Properties {
		newProp, ok := newS.Properties[name]
		if !ok {
			out = append(out, fmt.Sprintf("%s: поле %s%s удалено", file, path, name))
			continue
		}
		if fmt.Sprint(oldProp.Type) != fmt.Sprint(newProp.Type) {
			out = append(out, fmt.Sprintf("%s: поле %s%s сменило тип %v → %v", file, path, name, oldProp.Type, newProp.Type))
		}
		if len(oldProp.Enum) > 0 && len(newProp.Enum) > 0 {
			allowed := map[string]bool{}
			for _, e := range newProp.Enum {
				allowed[fmt.Sprint(e)] = true
			}
			for _, e := range oldProp.Enum {
				if !allowed[fmt.Sprint(e)] {
					out = append(out, fmt.Sprintf("%s: поле %s%s: значение %v убрано из перечисления", file, path, name, e))
				}
			}
		}
		out = append(out, compare(file, path+name+".", oldProp, newProp)...)
	}
	return out
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, strings.TrimSpace(err.Error()))
	return 2
}
