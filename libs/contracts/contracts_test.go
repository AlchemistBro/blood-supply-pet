package contracts_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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
			var v any
			if ctor, ok := contracts.Registry[typeName]; ok {
				msg := ctor()
				if msg.MessageType() != typeName {
					t.Fatalf("MessageType()=%s, имя схемы %s", msg.MessageType(), typeName)
				}
				if want := "v" + strconv.Itoa(msg.SchemaVersion()); !strings.Contains(filepath.Base(schemaPath), "."+want+".") {
					t.Fatalf("SchemaVersion()=%d не совпадает с версией в имени схемы %s", msg.SchemaVersion(), schemaPath)
				}
				v = msg
			} else if ctor, ok := contracts.Inbound[typeName]; ok {
				v = ctor()
			} else {
				t.Fatalf("тип %s не зарегистрирован ни в contracts.Registry, ни в contracts.Inbound", typeName)
			}
			seenTypes[typeName] = true
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
	for name := range contracts.Inbound {
		if !seenTypes[name] {
			t.Errorf("входящий тип %s зарегистрирован, но схемы и примера нет", name)
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

// Удаление любого обязательного поля из примера (на любом уровне вложенности) должно ломать валидацию:
// иначе схема не защищает получателя, а «required» в ней декоративный.
func TestSchemasRejectMissingRequiredFields(t *testing.T) {
	schemas, err := filepath.Glob("schemas/*/*.json")
	if err != nil || len(schemas) == 0 {
		t.Fatalf("схемы не найдены: %v", err)
	}
	for _, schemaPath := range schemas {
		rel := strings.TrimPrefix(filepath.ToSlash(schemaPath), "schemas/")
		t.Run(rel, func(t *testing.T) {
			schema, err := jsonschema.NewCompiler().Compile(schemaPath)
			if err != nil {
				t.Fatalf("схема не компилируется: %v", err)
			}
			rawSchema := readJSON(t, schemaPath)
			example := readJSON(t, filepath.Join("examples", filepath.FromSlash(rel)))
			checked := 0
			forEachRequired(rawSchema, example, nil, func(path []string) {
				checked++
				broken := deepCopy(example)
				deletePath(broken, path)
				if err := schema.Validate(broken); err == nil {
					t.Errorf("пример без обязательного поля %s всё равно проходит схему", strings.Join(path, "."))
				}
			})
			if checked == 0 {
				t.Errorf("в схеме нет обязательных полей")
			}
		})
	}
}

// Номер случая пациента — врачебная тайна (152-ФЗ, ст. 10): в открытом виде он не появляется ни в одной
// схеме сообщений. Отдельный тест, чтобы правило не потерялось при правке общего списка ниже.
func TestNoSchemaHasPatientCaseNo(t *testing.T) {
	schemas, err := filepath.Glob("schemas/*/*.json")
	if err != nil || len(schemas) == 0 {
		t.Fatalf("схемы не найдены: %v", err)
	}
	for _, schemaPath := range schemas {
		raw, err := os.ReadFile(schemaPath)
		if err != nil {
			t.Fatal(err)
		}
		var node any
		if err := json.Unmarshal(raw, &node); err != nil {
			t.Fatalf("%s: %v", schemaPath, err)
		}
		if hasProperty(node, "patient_case_no") {
			t.Errorf("%s объявляет свойство patient_case_no", schemaPath)
		}
	}
}

// Номер случая ходит между сервисами только зашифрованным конвертом (ADR-0020): каждое поле patient_case_sealed
// объявлено строкой с шаблоном конверта, и открытый номер этот шаблон не проходит.
func TestPatientCaseSealedIsEnvelope(t *testing.T) {
	schemas, err := filepath.Glob("schemas/*/*.json")
	if err != nil || len(schemas) == 0 {
		t.Fatalf("схемы не найдены: %v", err)
	}
	found := 0
	for _, path := range schemas {
		var walk func(node map[string]any)
		walk = func(node map[string]any) {
			props, _ := node["properties"].(map[string]any)
			for name, raw := range props {
				prop, _ := raw.(map[string]any)
				if prop == nil {
					continue
				}
				if name == "patient_case_sealed" {
					found++
					if prop["type"] != "string" || prop["pattern"] != contracts.PatientCaseSealedPattern {
						t.Errorf("%s: patient_case_sealed должен быть строкой с шаблоном %s, а описан %v",
							path, contracts.PatientCaseSealedPattern, prop)
					}
				}
				walk(prop)
			}
			if items, ok := node["items"].(map[string]any); ok {
				walk(items)
			}
		}
		walk(readJSON(t, path))
	}
	if found < 2 {
		t.Fatalf("patient_case_sealed найден в %d схемах, ожидали OpenObligation и AllocateUnitsToPatient", found)
	}

	schema, err := jsonschema.NewCompiler().Compile("schemas/inventory/AllocateUnitsToPatient.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"2026/123", "123456", "v1..abc", "v2.kid.QUJDREVGR0hJSktMTU5PUA"} {
		v := readJSON(t, "examples/inventory/AllocateUnitsToPatient.v1.json")
		v["patient_case_sealed"] = plain
		if err := schema.Validate(v); err == nil {
			t.Errorf("значение %q прошло шаблон конверта", plain)
		}
	}
}

// hasProperty ищет имя среди ключей любого объекта properties на любом уровне схемы.
func hasProperty(node any, name string) bool {
	switch x := node.(type) {
	case map[string]any:
		if props, ok := x["properties"].(map[string]any); ok {
			if _, found := props[name]; found {
				return true
			}
		}
		for _, v := range x {
			if hasProperty(v, name) {
				return true
			}
		}
	case []any:
		for _, v := range x {
			if hasProperty(v, name) {
				return true
			}
		}
	}
	return false
}

// Регистрация типов разнесена по файлам registry_<topic>.go: повтор имени типа в двух файлах — ошибка.
func TestRegistryHasNoDuplicateTypes(t *testing.T) {
	if dups := contracts.DuplicateTypes(); len(dups) > 0 {
		t.Fatalf("типы зарегистрированы дважды: %v", dups)
	}
	for name, ctor := range contracts.Registry {
		if got := ctor().MessageType(); got != name {
			t.Errorf("ключ реестра %s, MessageType() %s", name, got)
		}
	}
}

// Контракты не несут ПДн, номер случая пациента и исходный код единицы АИСТ-М (ADR-0010, NFR010):
// ни одна схема не объявляет такие поля, на каком бы уровне они ни лежали.
func TestSchemasDeclareNoPersonalOrSourceIdentifiers(t *testing.T) {
	forbidden := map[string]bool{
		"patient_case_no": true, "patient_case_ref": true, "full_name": true, "snils": true,
		"aist_unit_code": true, "donation_no": true,
	}
	schemas, err := filepath.Glob("schemas/*/*.json")
	if err != nil || len(schemas) == 0 {
		t.Fatalf("схемы не найдены: %v", err)
	}
	for _, schemaPath := range schemas {
		var walk func(node any, path string)
		walk = func(node any, path string) {
			switch x := node.(type) {
			case map[string]any:
				if props, ok := x["properties"].(map[string]any); ok {
					for name := range props {
						if forbidden[name] {
							t.Errorf("%s: запрещённое поле %s%s", schemaPath, path, name)
						}
					}
				}
				for k, v := range x {
					walk(v, path+k+".")
				}
			case []any:
				for _, v := range x {
					walk(v, path)
				}
			}
		}
		walk(readJSON(t, schemaPath), "")
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

// forEachRequired обходит обязательные поля схемы, присутствующие в примере, и спускается во вложенные объекты.
func forEachRequired(schema, example map[string]any, prefix []string, fn func(path []string)) {
	props, _ := schema["properties"].(map[string]any)
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			name, _ := r.(string)
			if _, present := example[name]; present {
				fn(append(append([]string(nil), prefix...), name))
			}
		}
	}
	for name, sub := range props {
		subSchema, ok1 := sub.(map[string]any)
		subExample, ok2 := example[name].(map[string]any)
		if ok1 && ok2 {
			forEachRequired(subSchema, subExample, append(append([]string(nil), prefix...), name), fn)
		}
	}
}

func deepCopy(v map[string]any) map[string]any {
	raw, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func deletePath(v map[string]any, path []string) {
	for _, p := range path[:len(path)-1] {
		v, _ = v[p].(map[string]any)
		if v == nil {
			return
		}
	}
	delete(v, path[len(path)-1])
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
