package ui

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed templates
var templatesFS embed.FS

// Встроенные страницы пакета.
const (
	// ConfirmPage — страница подтверждения без JS; Page.Content — Confirm, Page.Title — вопрос.
	ConfirmPage = "ui/confirm"
	// ErrorPage — страница ошибки (403, 404, 500); Page.Content — ErrorView.
	ErrorPage = "ui/error"
)

// Options — настройка набора шаблонов.
type Options struct {
	// Pages — шаблоны экранов. Каждый файл *.html определяет "content" и, если нужно, "page_actions";
	// имя страницы — путь файла в Pages ("operator/tasks.html"). Может быть nil.
	Pages fs.FS
	// StaticPrefix — путь, по которому смонтирован StaticHandler; по умолчанию "/static/".
	StaticPrefix string
	// AssetVersion — метка версии в адресах статики (?v=…). Пусто — 12 знаков SHA-256 от содержимого статики.
	AssetVersion string
}

// Renderer — разобранные шаблоны: базовый набор компонентов и по клону на каждую страницу.
// Всё разбирается в New: ошибка в шаблоне ломает запуск, а не запрос пользователя.
type Renderer struct {
	components *template.Template
	pages      map[string]*template.Template
	prefix     string
	version    string
}

// New разбирает шаблоны пакета и экранов.
func New(opts Options) (*Renderer, error) {
	r := &Renderer{prefix: opts.StaticPrefix, version: opts.AssetVersion, pages: map[string]*template.Template{}}
	if r.prefix == "" {
		r.prefix = "/static/"
	}
	if !strings.HasSuffix(r.prefix, "/") {
		r.prefix += "/"
	}
	if r.version == "" {
		v, err := contentVersion()
		if err != nil {
			return nil, err
		}
		r.version = v
	}

	base, err := template.New("ui").Funcs(r.funcs()).ParseFS(templatesFS, "templates/*.html", "templates/components/*.html")
	if err != nil {
		return nil, fmt.Errorf("ui: разбор базовых шаблонов: %w", err)
	}

	builtin := map[string]string{ConfirmPage: "templates/pages/confirm.html", ErrorPage: "templates/pages/error.html"}
	for name, file := range builtin {
		if err := r.addPage(base, name, templatesFS, file); err != nil {
			return nil, err
		}
	}
	if opts.Pages != nil {
		err := fs.WalkDir(opts.Pages, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".html") {
				return nil
			}
			if _, dup := r.pages[path]; dup {
				return fmt.Errorf("ui: страница %q уже есть", path)
			}
			return r.addPage(base, path, opts.Pages, path)
		})
		if err != nil {
			return nil, fmt.Errorf("ui: шаблоны экранов: %w", err)
		}
	}

	// Компоненты исполняются отдельно от клонов страниц: исполненный шаблон html/template клонировать нельзя.
	r.components, err = base.Clone()
	if err != nil {
		return nil, fmt.Errorf("ui: клон компонентов: %w", err)
	}
	return r, nil
}

func (r *Renderer) addPage(base *template.Template, name string, fsys fs.FS, file string) error {
	t, err := base.Clone()
	if err != nil {
		return fmt.Errorf("ui: клон для %q: %w", name, err)
	}
	src, err := fs.ReadFile(fsys, file)
	if err != nil {
		return fmt.Errorf("ui: чтение %q: %w", file, err)
	}
	if _, err := t.New(name).Parse(string(src)); err != nil {
		return fmt.Errorf("ui: разбор %q: %w", name, err)
	}
	if t.Lookup("content") == nil {
		return fmt.Errorf("ui: страница %q не определяет \"content\"", name)
	}
	r.pages[name] = t
	return nil
}

// Page рендерит полную страницу name с макетом.
func (r *Renderer) Page(w io.Writer, name string, p Page) error {
	return r.Partial(w, name, "layout", p)
}

// Partial рендерит один шаблон страницы name (например, "content" или строку таблицы для ответа htmx).
func (r *Renderer) Partial(w io.Writer, name, tmpl string, data any) error {
	t, ok := r.pages[name]
	if !ok {
		return fmt.Errorf("%w %q", ErrNoPage, name)
	}
	return execute(w, t, tmpl, data)
}

// Component рендерит компонент пакета: "badge", "table", "field", "confirm" и т. д.
func (r *Renderer) Component(w io.Writer, name string, data any) error {
	return execute(w, r.components, name, data)
}

// WritePage рендерит страницу в буфер и только затем пишет ответ: ошибка шаблона не оставляет
// пользователю половину страницы, а вызывающий может ответить ErrorPage.
func (r *Renderer) WritePage(w http.ResponseWriter, status int, name string, p Page) error {
	var buf bytes.Buffer
	if err := r.Page(&buf, name, p); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		return fmt.Errorf("ui: запись ответа: %w", err)
	}
	return nil
}

func execute(w io.Writer, t *template.Template, name string, data any) error {
	if t.Lookup(name) == nil {
		return fmt.Errorf("ui: нет шаблона %q", name)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		return fmt.Errorf("ui: шаблон %q: %w", name, err)
	}
	if _, err := buf.WriteTo(w); err != nil {
		return fmt.Errorf("ui: запись %q: %w", name, err)
	}
	return nil
}

// Asset — адрес файла статики с меткой версии: браузер кэширует надолго и сбрасывает кэш с новой сборкой.
func (r *Renderer) Asset(path string) string {
	return r.prefix + strings.TrimPrefix(path, "/") + "?v=" + r.version
}

// AssetVersion — метка версии статики.
func (r *Renderer) AssetVersion() string { return r.version }

// funcs — функции шаблонов. Ни одна не возвращает доверенные типы html/template: всё, что приходит из данных,
// экранирует html/template.
func (r *Renderer) funcs() template.FuncMap {
	return template.FuncMap{
		"asset":       r.Asset,
		"datetime":    FormatDateTime,
		"date":        FormatDate,
		"hm":          FormatTime,
		"iso":         isoTime,
		"ml":          FormatML,
		"duration":    FormatDuration,
		"statusBadge": statusBadgeAny,
		"statusLabel": func(s any) string { return statusBadgeAny(s).Label },
		"roleLabel":   RoleLabel,
		"csrfHeaders": csrfHeaders,
		"inDialog":    func(c Confirm) confirmView { return confirmView{Confirm: c, InDialog: true} },
		"onPage":      func(c Confirm) confirmView { return confirmView{Confirm: c} },
	}
}

// statusBadgeAny принимает и ObligationStatus, и строку из ответа сервиса: text/template не приводит типы сам.
func statusBadgeAny(s any) Badge {
	switch v := s.(type) {
	case ObligationStatus:
		return StatusBadge(v)
	case string:
		return StatusBadge(ObligationStatus(v))
	default:
		return StatusBadge(ObligationStatus(fmt.Sprint(v)))
	}
}

// isoTime — машинное время для атрибута datetime у <time>.
func isoTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// csrfHeaders — JSON для hx-headers: htmx отправляет токен заголовком X-CSRF-Token (ADR-0018).
// Строится json.Marshal, а не склейкой строк; в атрибут его экранирует html/template.
func csrfHeaders(token string) (string, error) {
	b, err := json.Marshal(map[string]string{"X-CSRF-Token": token})
	if err != nil {
		return "", fmt.Errorf("ui: заголовки htmx: %w", err)
	}
	return string(b), nil
}

// ErrorView — данные страницы ошибки.
type ErrorView struct {
	Status  int
	Text    string
	BackURL string
}

// ForbiddenPage — 403 «Недостаточно прав». Запись DENIED в аудит делает вызывающий (этап 2, WP1).
func ForbiddenPage(base Page) (Page, int) {
	base.Title = "Недостаточно прав"
	base.Content = ErrorView{
		Status:  http.StatusForbidden,
		Text:    "У вашей роли нет доступа к этому разделу. Если доступ нужен для работы, обратитесь во внешнюю ИТ-службу.",
		BackURL: "/",
	}
	return base, http.StatusForbidden
}

// NotFoundPage — 404.
func NotFoundPage(base Page) (Page, int) {
	base.Title = "Страница не найдена"
	base.Content = ErrorView{
		Status:  http.StatusNotFound,
		Text:    "Такой страницы нет. Проверьте адрес или вернитесь на главную.",
		BackURL: "/",
	}
	return base, http.StatusNotFound
}

// ServerErrorPage — 500 без подробностей: подробности только в журнале с correlation_id.
func ServerErrorPage(base Page) (Page, int) {
	base.Title = "Не удалось выполнить действие"
	base.Content = ErrorView{
		Status:  http.StatusInternalServerError,
		Text:    "Произошла внутренняя ошибка. Повторите через минуту; если ошибка повторяется, сообщите во внешнюю ИТ-службу.",
		BackURL: "/",
	}
	return base, http.StatusInternalServerError
}

// ErrNoPage — страницы нет в наборе.
var ErrNoPage = errors.New("ui: нет страницы")
