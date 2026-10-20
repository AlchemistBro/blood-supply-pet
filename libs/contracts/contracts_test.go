package contracts_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
)

// Для каждой схемы schemas/<topic>/<Type>.vN.json должен быть пример examples/<topic>/<Type>.vN.json,
// пример валиден по схеме, а Go-тип из Registry переживает цикл JSON → Go → JSON без потерь.
func TestExamplesMatchSchemasAndGoTypes(t *testing.T) {
	schemas, err := filepath.Glob("schemas/*/*.json")
	if err != nil || len(schemas) == 0 {
		t.Fatalf("схемы не найдены: %v", err)
	}
	seenTypes := map[string]bool{}
	for _, schemaPath := range schemas {
		rel := strings.TrimPrefix(filepath.ToSlash(schemaPath), "schemas/")
		examplePath := filepath.Join("examples", filepath.FromSlash(rel))
		t.Run(rel, func(t *testing.T) {
			compiler := jsonschema.NewCompiler()
			schema, err := compiler.Compile(schemaPath)
			if err != nil {
				t.Fatalf("схема не компилируется: %v", err)
			}
			raw, err := os.ReadFile(examplePath)
			if err != nil {
				t.Fatalf("нет примера %s: %v", examplePath, err)
			}
			var example any
			if err := json.Unmarshal(raw, &example); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(example); err != nil {
				t.Fatalf("пример не проходит схему: %v", err)
			}

			typeName := strings.SplitN(filepath.Base(schemaPath), ".", 2)[0]
			ctor, ok := contracts.Registry[typeName]
			if !ok {
				t.Fatalf("тип %s не зарегистрирован в contracts.Registry", typeName)
			}
			seenTypes[typeName] = true
			v := ctor()
			if v.MessageType() != typeName {
				t.Fatalf("MessageType()=%s, имя схемы %s", v.MessageType(), typeName)
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(v); err != nil {
				t.Fatalf("Go-тип не принимает пример (лишние или неизвестные поля): %v", err)
			}
			back, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			var roundtrip any
			if err := json.Unmarshal(back, &roundtrip); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(roundtrip); err != nil {
				t.Fatalf("сериализация Go-типа не проходит схему: %v", err)
			}
			if !reflect.DeepEqual(normalize(example), normalize(roundtrip)) {
				t.Fatalf("Go-тип теряет данные:\nпример   %s\nобратно  %s", raw, back)
			}
		})
	}
	for name := range contracts.Registry {
		if !seenTypes[name] {
			t.Errorf("тип %s зарегистрирован, но схемы и примера нет", name)
		}
	}
}

// normalize убирает пустые поля, которые Go опускает через omitempty.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			n := normalize(val)
			if n == nil {
				continue
			}
			out[k] = n
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, val := range x {
			out = append(out, normalize(val))
		}
		return out
	default:
		return v
	}
}

func TestTopicsAreUniqueAndOwnedNames(t *testing.T) {
	seen := map[string]bool{}
	for _, topic := range contracts.AllTopics {
		if seen[topic] {
			t.Errorf("топик %s повторяется", topic)
		}
		seen[topic] = true
		parts := strings.Split(topic, ".")
		if len(parts) != 2 || (parts[1] != "events" && parts[1] != "commands" && parts[1] != "replies") {
			t.Errorf("имя топика %s не по шаблону <владелец>.<events|commands|replies>", topic)
		}
	}
}
