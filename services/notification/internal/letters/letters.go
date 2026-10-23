// Package letters собирает письма по шаблонам: текстовую и HTML-часть в строгом служебном стиле, на русском.
//
// Шаблон получает только строки, подготовленные здесь из domain.Facts: номер задания, клиники, вид компонента,
// количество, срочность, срок, причину и ссылку на экран web. Номера случая пациента, фенотипа и ФИО в данных шаблона
// нет (FR044, NFR016, ADR-0011). Каждое письмо помечено «демо» (ADR-0010).
package letters

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"net/url"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/transfer"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/domain"
)

//go:embed templates/*.tmpl
var templateFiles embed.FS

// msk — московское время для сроков в письме: UTC+3 без перехода на летнее время с 2014 года. Фиксированная зона
// не требует базы часовых поясов в образе.
var msk = time.FixedZone("МСК", 3*60*60)

// Letter — готовое письмо.
type Letter struct {
	Subject string
	Text    string
	HTML    string
}

type row struct {
	Label string
	Value string
}

// view — данные шаблона: только готовые строки.
type view struct {
	Title  string
	Lead   string
	Action string
	Link   string
	Rows   []row
}

// Renderer собирает письма. Безопасен для параллельного использования.
type Renderer struct {
	clinics map[int64]refdata.Clinic
	kinds   map[string]string
	urgency map[string]string
	baseURL string
	text    *texttemplate.Template
	html    *htmltemplate.Template
}

// New готовит шаблоны. webBaseURL — адрес интерфейса web для ссылок (http или https).
func New(ref *refdata.Set, webBaseURL string) (*Renderer, error) {
	u, err := url.Parse(webBaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("адрес web для ссылок в письмах %q: нужен http(s)://хост", webBaseURL)
	}
	text, err := texttemplate.ParseFS(templateFiles, "templates/letter.txt.tmpl")
	if err != nil {
		return nil, fmt.Errorf("шаблон письма (text): %w", err)
	}
	html, err := htmltemplate.ParseFS(templateFiles, "templates/letter.html.tmpl")
	if err != nil {
		return nil, fmt.Errorf("шаблон письма (html): %w", err)
	}
	r := &Renderer{
		clinics: map[int64]refdata.Clinic{}, kinds: map[string]string{}, urgency: map[string]string{},
		baseURL: strings.TrimRight(webBaseURL, "/"), text: text, html: html,
	}
	for _, c := range ref.Clinics {
		r.clinics[c.ClinicID] = c
	}
	for _, k := range ref.ComponentKinds {
		r.kinds[k.Code] = k.Name
	}
	for _, u := range ref.Urgency {
		r.urgency[u.Code] = u.Name
	}
	return r, nil
}

// Subject — тема письма: вид и короткий номер задания (первые 8 знаков идентификатора).
func (r *Renderer) Subject(kind domain.Kind, f domain.Facts) string {
	short := f.ObligationID.String()[:8]
	switch kind {
	case domain.KindUrgentTask:
		return "[демо] Срочное задание на передачу № " + short
	case domain.KindEscalation:
		return "[демо] Эскалация по заданию № " + short
	case domain.KindTransferBroken:
		return "[демо] Нарушено обязательство на передачу № " + short
	default:
		return "[демо] Уведомление по заданию № " + short
	}
}

// ErrUnknownKind — вид письма без шаблона.
var ErrUnknownKind = errors.New("неизвестный вид письма")

// Render собирает письмо.
func (r *Renderer) Render(kind domain.Kind, f domain.Facts) (Letter, error) {
	v, err := r.view(kind, f)
	if err != nil {
		return Letter{}, err
	}
	var text, html bytes.Buffer
	if err := r.text.Execute(&text, v); err != nil {
		return Letter{}, fmt.Errorf("письмо %s (text): %w", kind, err)
	}
	if err := r.html.Execute(&html, v); err != nil {
		return Letter{}, fmt.Errorf("письмо %s (html): %w", kind, err)
	}
	return Letter{Subject: r.Subject(kind, f), Text: text.String(), HTML: html.String()}, nil
}

func (r *Renderer) view(kind domain.Kind, f domain.Facts) (view, error) {
	id := f.ObligationID.String()
	var v view
	switch kind {
	case domain.KindUrgentTask:
		v = view{
			Title:  "Срочное задание на передачу",
			Lead:   "Врач выбрал вашу клинику источником по заявке. Подтвердите передачу единиц компонента крови или откажите до указанного срока.",
			Action: "Требования к единицам смотрите в карточке задания в системе.",
			Link:   r.baseURL + "/operator/tasks/" + url.PathEscape(id),
		}
	case domain.KindEscalation:
		v = view{
			Title:  "Эскалация дежурному смены",
			Lead:   "Ситуация по заданию на передачу требует решения дежурного смены.",
			Action: "Решение принимается на экране «Эскалации»: назначить перевозку вручную или закрыть с комментарием.",
			Link:   r.baseURL + "/duty/escalations",
		}
	case domain.KindTransferBroken:
		v = view{
			Title:  "Нарушено обязательство на передачу",
			Lead:   "Закреплённая единица компонента крови стала недоступна: обязательство на передачу нарушено, закрепление единицы снято.",
			Action: "Проверьте задание и последствия для заявки на экране «Все задания».",
			Link:   r.baseURL + "/duty/tasks",
		}
	default:
		return view{}, fmt.Errorf("%w: %s", ErrUnknownKind, kind)
	}
	v.Rows = append(v.Rows, row{"Задание", id})
	if f.Reason != "" {
		v.Rows = append(v.Rows, row{"Причина", reasonText(f.Reason)})
	}
	if f.SourceClinicID != nil {
		v.Rows = append(v.Rows, row{"Клиника-источник", r.clinic(*f.SourceClinicID)})
	}
	if f.TargetClinicID != nil {
		v.Rows = append(v.Rows, row{"Клиника-получатель", r.clinic(*f.TargetClinicID)})
	}
	if f.ComponentKind != "" {
		v.Rows = append(v.Rows, row{"Вид компонента", r.kind(f.ComponentKind)})
	}
	if f.Quantity != nil {
		v.Rows = append(v.Rows, row{"Количество", strconv.Itoa(*f.Quantity) + " ед."})
	}
	if f.Urgency != "" {
		v.Rows = append(v.Rows, row{"Срочность", lookup(r.urgency, f.Urgency)})
	}
	if f.Deadline != nil {
		v.Rows = append(v.Rows, row{"Срок подтверждения", f.Deadline.In(msk).Format("02.01.2006 15:04") + " МСК"})
	}
	return v, nil
}

func (r *Renderer) clinic(id int64) string {
	c, ok := r.clinics[id]
	if !ok {
		return "клиника " + strconv.FormatInt(id, 10)
	}
	return c.ClinicCode + " — " + c.ClinicName
}

func (r *Renderer) kind(code string) string {
	name, ok := r.kinds[code]
	if !ok {
		return code
	}
	return name + " (" + code + ")"
}

func lookup(m map[string]string, code string) string {
	if name, ok := m[code]; ok {
		return name
	}
	return code
}

// reasons — причины эскалации (EscalationRaised) и нарушения (TransferBroken) человеческим языком.
var reasons = map[string]string{
	transfer.EscalationExpiredNoFallback:       "срок подтверждения истёк, запасного источника нет",
	transfer.EscalationTransportRefused:        "служба перевозки отказала в заказе",
	transfer.EscalationTransportOutcomeUnknown: "исход заказа перевозки неизвестен",
	transfer.EscalationSourceSystemUnavailable: "АИСТ-М недоступна, экстренное задание не подтверждено",
	transfer.BrokenIssued:                      "закреплённая единица выдана",
	transfer.BrokenDiscarded:                   "закреплённая единица списана",
	transfer.BrokenTransferred:                 "закреплённая единица передана в другую организацию",
	transfer.BrokenExpired:                     "истёк срок годности закреплённой единицы",
	transfer.BrokenQuarantine:                  "закреплённая единица переведена на карантин",
}

func reasonText(code string) string {
	return lookup(reasons, code)
}
