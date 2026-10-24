package ui

import (
	"bytes"
	"io/fs"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

// Нарушения CSP без 'unsafe-inline' (ADR-0018): встроенный скрипт, стиль, обработчик события.
var inlineRules = []struct {
	name string
	re   *regexp.Regexp
}{
	{"<style>", regexp.MustCompile(`(?i)<style\b`)},
	{"атрибут style", regexp.MustCompile(`(?i)\sstyle\s*=`)},
	{"обработчик on*", regexp.MustCompile(`(?i)\son[a-z]+\s*=`)},
	{"hx-on", regexp.MustCompile(`(?i)\bhx-on`)},
	{"javascript:", regexp.MustCompile(`(?i)javascript:`)},
}

var scriptTag = regexp.MustCompile(`(?i)<script\b[^>]*>`)

func checkNoInline(t *testing.T, name, src string) {
	t.Helper()
	for _, tag := range scriptTag.FindAllString(src, -1) {
		if !strings.Contains(strings.ToLower(tag), "src=") {
			t.Errorf("%s: встроенный скрипт без src: %s", name, tag)
		}
	}
	for _, rule := range inlineRules {
		if loc := rule.re.FindStringIndex(src); loc != nil {
			t.Errorf("%s: %s: …%s…", name, rule.name, src[max(0, loc[0]-30):min(len(src), loc[1]+30)])
		}
	}
}

func TestTemplatesHaveNoInlineScriptsStylesOrHandlers(t *testing.T) {
	n := 0
	err := fs.WalkDir(templatesFS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(templatesFS, path)
		if err != nil {
			return err
		}
		n++
		checkNoInline(t, path, string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 5 {
		t.Fatalf("проверено шаблонов %d — встраивание сломано?", n)
	}
}

func TestRenderedPagesHaveNoInlineCode(t *testing.T) {
	r := newTestRenderer(t)
	var buf bytes.Buffer
	if err := r.Page(&buf, "demo.html", fixturePage()); err != nil {
		t.Fatal(err)
	}
	checkNoInline(t, "layout", buf.String())

	buf.Reset()
	c := fixtureConfirm()
	page := fixturePage()
	page.Title, page.Content = c.Question, c
	if err := r.Page(&buf, ConfirmPage, page); err != nil {
		t.Fatal(err)
	}
	checkNoInline(t, "confirm page", buf.String())

	for name, data := range map[string]any{
		"table":   fixtureTable(t),
		"confirm": fixtureConfirm(),
	} {
		checkNoInline(t, name, renderComponent(t, r, name, data))
	}
}

// Пакет не строит доверенные типы html/template: всё экранируется стандартно.
func TestCodeDoesNotBypassEscaping(t *testing.T) {
	trusted := regexp.MustCompile(`template\.(HTML|HTMLAttr|JS|JSStr|CSS|URL|Srcset)\b`)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if m := trusted.Find(b); m != nil {
			t.Errorf("%s: %s обходит экранирование", name, m)
		}
	}
}

// В системе нет ФИО: у модели пользователя только логин и роль, шапка выводит только их и клинику.
func TestHeaderHasNoPersonalNameFields(t *testing.T) {
	fields := func(v any) []string {
		var out []string
		rt := reflect.TypeOf(v)
		for i := range rt.NumField() {
			out = append(out, rt.Field(i).Name)
		}
		return out
	}
	if got := fields(User{}); !slices.Equal(got, []string{"Login", "Role"}) {
		t.Fatalf("поля User: %v — новые поля о человеке только после проверки правил канона", got)
	}
	if got := fields(Header{}); !slices.Equal(got, []string{"User", "Clinic", "Freshness", "HomeURL", "LogoutURL"}) {
		t.Fatalf("поля Header: %v", got)
	}

	var buf bytes.Buffer
	if err := newTestRenderer(t).Page(&buf, "demo.html", fixturePage()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	start := strings.Index(out, `<div class="topbar__user">`)
	end := strings.Index(out, `</header>`)
	if start < 0 || end < start {
		t.Fatal("нет блока пользователя в шапке")
	}
	block := out[start:end]
	tags := regexp.MustCompile(`<[^>]+>`)
	var texts []string
	for _, s := range tags.Split(block, -1) {
		if s = strings.TrimSpace(s); s != "" && s != "·" {
			texts = append(texts, s)
		}
	}
	want := []string{"Роль:", "Оператор клинической организации", "Клиника:", "Клиника № 1", "Логин:", "operator-c001", "Выйти"}
	if !slices.Equal(texts, want) {
		t.Fatalf("шапка выводит %q, ожидалось только %q", texts, want)
	}
}

const xss = `<script>alert(1)</script>`

func TestUserStringsAreEscaped(t *testing.T) {
	r := newTestRenderer(t)
	p := fixturePage()
	p.Title = xss
	p.Header.User = User{Login: `op"><img src=x onerror=alert(1)>`, Role: auth.RoleOperator}
	p.Header.Clinic = xss
	p.Breadcrumbs = []Breadcrumb{{Label: xss, URL: "javascript:alert(1)"}}
	p.Alerts = []Alert{{Tone: ToneWarning, Text: xss}}
	p.Flashes = []Flash{{Tone: ToneInfo, Text: xss}}
	p.CSRFToken = `"}' onload='alert(1)`

	var buf bytes.Buffer
	if err := r.Page(&buf, "demo.html", p); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, xss) || strings.Contains(out, "<img src=x") {
		t.Fatal("пользовательская строка попала в разметку без экранирования")
	}
	if !strings.Contains(out, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatal("ожидалось экранирование &lt;script&gt;")
	}
	if !strings.Contains(out, `href="#ZgotmplZ"`) {
		t.Fatal("javascript: в ссылке должен заменяться html/template на #ZgotmplZ")
	}
	if strings.Contains(out, `onload='alert`) || strings.Contains(out, `value=""}'`) {
		t.Fatal("CSRF-токен вышел из атрибута")
	}

	tbl := fixtureTable(t)
	tbl.Rows[0].ID = `x" onmouseover="alert(1)`
	tbl.Rows[0].Cells[0] = Cell{Text: xss, Href: "javascript:alert(1)"}
	cell := renderComponent(t, r, "table", tbl)
	if strings.Contains(cell, xss) || strings.Contains(cell, `" onmouseover="`) || !strings.Contains(cell, "#ZgotmplZ") {
		t.Fatalf("ячейка таблицы не экранирована: %s", cell)
	}

	field := renderComponent(t, r, "field", Field{ID: "f", Name: "f", Label: xss, Value: `"><script>alert(1)</script>`, Error: xss})
	if strings.Contains(field, "<script>") {
		t.Fatalf("поле не экранировано: %s", field)
	}
}
