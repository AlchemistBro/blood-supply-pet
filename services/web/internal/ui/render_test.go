package ui

import (
	"bytes"
	"errors"
	"flag"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

// -update перезаписывает testdata/*.golden текущим выводом. Сравнение построчное без отступов и пустых строк:
// golden проверяет разметку, а не пробелы, которые html/template оставляет вокруг {{if}} и {{range}}.
var update = flag.Bool("update", false, "перезаписать testdata/*.golden")

const demoPage = `{{define "content"}}<p>Содержимое экрана</p>{{end}}` +
	`{{define "page_actions"}}<a class="btn btn--primary" href="/operator/tasks/new">Создать</a>{{end}}`

func newTestRenderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := New(Options{
		Pages:        fstest.MapFS{"demo.html": {Data: []byte(demoPage)}},
		StaticPrefix: "/static/",
		AssetVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func normalizeHTML(s string) string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n") + "\n"
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	got = normalizeHTML(got)
	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("нет %s (%v): go test ./services/web/internal/ui -run %s -update", path, err, t.Name())
	}
	if w := normalizeHTML(string(want)); got != w {
		t.Fatalf("%s не совпал с выводом.\n--- got ---\n%s--- want ---\n%s", path, got, w)
	}
}

func renderComponent(t *testing.T, r *Renderer, name string, data any) string {
	t.Helper()
	var buf bytes.Buffer
	if err := r.Component(&buf, name, data); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// fixturePage — страница оператора: запас устарел (2 ч при пороге 15 мин), АИСТ-М недоступна, одно уведомление.
func fixturePage() Page {
	now := time.Date(2026, 10, 23, 11, 32, 0, 0, time.UTC)
	return Page{
		Title: "Задания на передачу",
		Header: Header{
			User:      User{Login: "operator-c001", Role: auth.RoleOperator},
			Clinic:    "Клиника № 1",
			Freshness: NewFreshness(now.Add(-2*time.Hour), now, 15*time.Minute),
		},
		Nav: []NavItem{
			{Label: "Задания", URL: "/operator/tasks", Active: true, Count: 2},
			{Label: "Запас", URL: "/operator/stock"},
		},
		Breadcrumbs: []Breadcrumb{{Label: "Главная", URL: "/"}, {Label: "Задания на передачу"}},
		Alerts:      []Alert{{Tone: ToneDanger, Text: "АИСТ-М недоступна: подтверждение временно невозможно"}},
		Flashes:     []Flash{{Tone: ToneSuccess, Text: "Закреплено 2 единицы. Готовьте к отправке."}},
		Footer:      Footer{Version: "v0.2.0", RepoURL: "https://github.com/AlchemistBro/blood-supply-pet"},
		CSRFToken:   "tok123",
	}
}

func TestGoldenLayout(t *testing.T) {
	r := newTestRenderer(t)
	var buf bytes.Buffer
	if err := r.Page(&buf, "demo.html", fixturePage()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		`<html lang="ru">`,
		`Запас обновлён <time datetime="2026-10-23T09:32:00Z">12:32</time>`,
		`topbar__watermark--stale`,
		`Данные запаса устарели (обновлено 2 ч назад)`,
		`<div class="band band--danger" role="alert">АИСТ-М недоступна: подтверждение временно невозможно</div>`,
		`<a href="/operator/tasks" aria-current="page">`,
		`<li aria-current="page">Задания на передачу</li>`,
		`<input type="hidden" name="csrf_token" value="tok123">`,
		`hx-headers="{&#34;X-CSRF-Token&#34;:&#34;tok123&#34;}"`,
		`Демонстрационная система. Данные и правила синтетические. Клиническое решение принимает врач.`,
		`<a class="btn btn--primary" href="/operator/tasks/new">Создать</a>`,
		`<p>Содержимое экрана</p>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в странице нет %q", want)
		}
	}
	assertGolden(t, "layout", out)
}

func TestGoldenForbiddenPageAnonymous(t *testing.T) {
	r := newTestRenderer(t)
	p, status := ForbiddenPage(Page{Footer: Footer{Version: "v0.2.0"}})
	if status != 403 {
		t.Fatalf("status %d", status)
	}
	var buf bytes.Buffer
	if err := r.Page(&buf, ErrorPage, p); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "topbar__user") || strings.Contains(out, "stock-watermark") || strings.Contains(out, "hx-headers") {
		t.Error("без входа шапка не показывает пользователя, водяной знак и CSRF-заголовок")
	}
	assertGolden(t, "page_forbidden", out)
}

func TestGoldenStatusBadges(t *testing.T) {
	r := newTestRenderer(t)
	var parts []string
	for _, s := range AllObligationStatuses() {
		parts = append(parts, renderComponent(t, r, "badge", StatusBadge(s)))
	}
	parts = append(parts,
		renderComponent(t, r, "badge", PartialBadge(2, 3)),
		renderComponent(t, r, "badge", AistPendingBadge()),
		renderComponent(t, r, "badge", AistAckBadge()),
	)
	assertGolden(t, "badges", strings.Join(parts, "\n"))
}

func TestStatusBadgeFuncAcceptsStringAndType(t *testing.T) {
	r, err := New(Options{
		Pages: fstest.MapFS{"s.html": {Data: []byte(
			`{{define "content"}}{{template "badge" (statusBadge "BROKEN")}}|{{statusLabel .Content}}{{end}}`)}},
		AssetVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := r.Partial(&buf, "s.html", "content", Page{Content: StatusDelivered}); err != nil {
		t.Fatal(err)
	}
	want := `<span class="badge badge--danger">Нарушено: единица выбыла</span>|Доставлено`
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}
}

func fixtureTable(t *testing.T) Table {
	base := mustURL(t, "/operator/tasks?status=PENDING&sort=deadline&dir=asc")
	cols := SortColumns([]Column{
		{Key: "no", Label: "Задание"},
		{Key: "deadline", Label: "Срок", Sortable: true},
		{Key: "qty", Label: "Количество, ед.", Numeric: true, Sortable: true},
		{Key: "status", Label: "Статус"},
	}, base, Sort{Key: "deadline", Dir: SortAsc})
	return Table{
		ID:      "tasks",
		Caption: "Задания на передачу",
		Columns: cols,
		Rows: []Row{
			{ID: "o-7f3a", Cells: []Cell{
				{Text: "7f3a", Href: "/operator/tasks/7f3a"},
				{Text: "23.10.2026 15:44"},
				{Text: "3"},
				{Badges: []Badge{StatusBadge(StatusPending), PartialBadge(2, 3)}},
			}},
			{ID: "o-9b21", Cells: []Cell{
				{Text: "9b21", Href: "/operator/tasks/9b21"},
				{Text: "23.10.2026 18:00"},
				{Text: "1"},
				{Badges: []Badge{StatusBadge(StatusTransportRequested)}},
			}},
		},
		Pagination: NewPagination(base, 1, 2, 5),
	}
}

func TestGoldenTableSortedWithPagination(t *testing.T) {
	r := newTestRenderer(t)
	out := renderComponent(t, r, "table", fixtureTable(t))
	for _, want := range []string{
		`<th scope="col" aria-sort="ascending">`,
		`href="/operator/tasks?dir=desc&amp;sort=deadline&amp;status=PENDING"`,
		`<th scope="col" class="num">`,
		`<td class="num">3</td>`,
		`<thead hx-boost="true" hx-target="#tasks" hx-select="#tasks" hx-swap="outerHTML">`,
		`<p class="pagination__summary">Показаны 1–2 из 5</p>`,
		`aria-current="page">1</a>`,
		`rel="next">Далее</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в таблице нет %q", want)
		}
	}
	assertGolden(t, "table", out)
}

func TestTableWithoutIDHasNoHTMX(t *testing.T) {
	r := newTestRenderer(t)
	tbl := fixtureTable(t)
	tbl.ID = ""
	out := renderComponent(t, r, "table", tbl)
	if strings.Contains(out, "hx-") {
		t.Fatal("без ID таблица работает обычными ссылками, без hx-атрибутов")
	}
	if !strings.Contains(out, `<a class="sort-link" href=`) {
		t.Fatal("ссылки сортировки остаются")
	}
}

func TestGoldenTableEmpty(t *testing.T) {
	r := newTestRenderer(t)
	tbl := Table{
		ID:         "tasks",
		Columns:    []Column{{Key: "no", Label: "Задание"}},
		Empty:      EmptyState{Text: "Заданий на передачу нет", ActionLabel: "Обновить", ActionURL: "/operator/tasks"},
		Pagination: NewPagination(&url.URL{Path: "/operator/tasks"}, 1, 20, 0),
	}
	out := renderComponent(t, r, "table", tbl)
	if strings.Contains(out, "<table") || strings.Contains(out, "pagination") {
		t.Fatal("без строк — только пустое состояние")
	}
	assertGolden(t, "table_empty", out)
}

func TestGoldenFormWithErrors(t *testing.T) {
	r := newTestRenderer(t)
	var errs FormErrors
	errs.Add("reason", "Причина отказа", "Укажите причину отказа")
	errs.Add("", "", "АИСТ-М не ответила. Задание остаётся в ожидании, повторите через минуту.")

	reason := Field{ID: "reason", Name: "reason", Label: "Причина отказа", Kind: KindTextarea, Required: true, Hint: "Увидит дежурный смены"}
	qty := Field{ID: "qty", Name: "qty", Label: "Количество, ед.", Kind: KindNumber, Value: "2"}
	kind := Field{ID: "kind", Name: "kind", Label: "Вид компонента", Kind: KindSelect, Options: []Option{
		{Value: "RBC", Label: "Эритроциты", Selected: true},
		{Value: "FFP", Label: "Плазма"},
	}}
	partial := Field{ID: "partial", Name: "partial", Label: "Подтвердить частично", Kind: KindCheckbox, Checked: true}
	errs.Apply(&reason, &qty, &kind, &partial)

	parts := []string{renderComponent(t, r, "error_summary", errs)}
	for _, f := range []Field{reason, qty, kind, partial} {
		parts = append(parts, renderComponent(t, r, "field", f))
	}
	out := strings.Join(parts, "\n")
	for _, want := range []string{
		`<a href="#reason">Причина отказа: Укажите причину отказа</a>`,
		`aria-invalid="true" aria-describedby="reason-hint reason-error"`,
		`<label for="qty">Количество, ед.</label>`,
		`<option value="RBC" selected>Эритроциты</option>`,
		`<input type="checkbox" id="partial" name="partial" value="on" checked>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в форме нет %q", want)
		}
	}
	if strings.Contains(out, " required") {
		t.Error("обязательность — aria-required, без браузерной проверки: ошибки по-русски отдаёт сервер")
	}
	assertGolden(t, "form", out)
}

func TestErrorSummaryEmptyRendersNothing(t *testing.T) {
	if out := renderComponent(t, newTestRenderer(t), "error_summary", FormErrors{}); strings.TrimSpace(out) != "" {
		t.Fatalf("got %q", out)
	}
}

func fixtureConfirm() Confirm {
	return Confirm{
		ID:           "reject-7f3a",
		TriggerLabel: "Отказать…",
		Question:     "Отказать в передаче?",
		Consequence:  "Задание будет отклонено, подбор предложит следующий источник.",
		Action:       "/operator/tasks/7f3a/reject",
		ConfirmLabel: "Отказать",
		CancelURL:    "/operator/tasks/7f3a",
		FallbackURL:  "/operator/tasks/7f3a/reject",
		Danger:       true,
		CSRFToken:    "tok123",
		Hidden:       []HiddenField{{Name: "idempotency_key", Value: "k-1"}},
		Fields:       []Field{{ID: "reject-reason", Name: "reason", Label: "Причина", Kind: KindTextarea, Required: true}},
	}
}

func TestGoldenConfirmDialog(t *testing.T) {
	out := renderComponent(t, newTestRenderer(t), "confirm", fixtureConfirm())
	for _, want := range []string{
		`href="/operator/tasks/7f3a/reject" data-dialog-open="reject-7f3a" aria-haspopup="dialog"`,
		`<dialog class="dialog" id="reject-7f3a" aria-labelledby="reject-7f3a-title">`,
		`<form class="confirm-form" method="post" action="/operator/tasks/7f3a/reject">`,
		`formmethod="dialog"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в диалоге нет %q", want)
		}
	}
	assertGolden(t, "confirm_dialog", out)
}

func TestGoldenConfirmFallbackPage(t *testing.T) {
	r := newTestRenderer(t)
	var buf bytes.Buffer
	c := fixtureConfirm()
	if err := r.Partial(&buf, ConfirmPage, "content", Page{Title: c.Question, Content: c}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "<dialog") || strings.Contains(out, `formmethod="dialog"`) {
		t.Fatal("страница-фолбэк работает без JS: без <dialog>, отмена — ссылка")
	}
	if !strings.Contains(out, `<a class="btn" href="/operator/tasks/7f3a">Отмена</a>`) {
		t.Fatal("нет ссылки отмены")
	}
	assertGolden(t, "confirm_page", out)
}

func TestGoldenFlashes(t *testing.T) {
	out := renderComponent(t, newTestRenderer(t), "flashes", []Flash{
		{Tone: ToneSuccess, Text: "Закреплено 2 единицы. Готовьте к отправке."},
		{Tone: ToneDanger, Text: "Единицы U-000124 уже закреплены за другим заданием. Выберите другие."},
	})
	assertGolden(t, "flashes", out)
}

func TestFlashesEmptyHasNoWhitespace(t *testing.T) {
	out := renderComponent(t, newTestRenderer(t), "flashes", []Flash(nil))
	if out != `<div class="flashes" id="flashes" aria-live="polite"></div>` {
		t.Fatalf("пустой контейнер должен быть :empty для CSS, got %q", out)
	}
}

func TestGoldenEmptyState(t *testing.T) {
	r := newTestRenderer(t)
	out := renderComponent(t, r, "empty", EmptyState{Text: "Заданий на передачу нет", ActionLabel: "Обновить", ActionURL: "/operator/tasks"})
	assertGolden(t, "empty", out)
	if def := renderComponent(t, r, "empty", EmptyState{}); !strings.Contains(def, "<p>Записей нет</p>") || strings.Contains(def, "<a") {
		t.Fatalf("по умолчанию — текст без кнопки: %q", def)
	}
}

func TestGoldenKVCard(t *testing.T) {
	out := renderComponent(t, newTestRenderer(t), "kv_card", Card{
		Title: "Параметры",
		Items: []KV{
			{Key: "Получатель", Value: "Клиника № 14"},
			{Key: "Количество, ед.", Value: "3", Numeric: true},
			{Key: "Статус", Badges: []Badge{StatusBadge(StatusConfirmed), AistPendingBadge()}},
			{Key: "Основание", Value: "Задание 7f3a", Href: "/operator/tasks/7f3a"},
		},
	})
	assertGolden(t, "kv_card", out)
}

func TestNewRejectsPageWithoutContent(t *testing.T) {
	_, err := New(Options{Pages: fstest.MapFS{"bad.html": {Data: []byte(`<p>нет content</p>`)}}, AssetVersion: "test"})
	if err == nil {
		t.Fatal("страница без \"content\" — ошибка при запуске, а не при запросе")
	}
}

func TestPartialUnknownPage(t *testing.T) {
	err := newTestRenderer(t).Partial(&bytes.Buffer{}, "nope.html", "content", nil)
	if !errors.Is(err, ErrNoPage) {
		t.Fatalf("got %v", err)
	}
}

func TestWritePageSetsTypeAndStatus(t *testing.T) {
	r := newTestRenderer(t)
	rec := httptest.NewRecorder()
	p, status := NotFoundPage(fixturePage())
	if err := r.WritePage(rec, status, ErrorPage, p); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 404 || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("code %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "<h1>Страница не найдена</h1>") {
		t.Fatal("нет заголовка 404")
	}
}
