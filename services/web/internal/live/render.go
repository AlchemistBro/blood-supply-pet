package live

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"strings"

	"github.com/google/uuid"
)

//go:embed templates/status.html
var templateFS embed.FS

var statusTmpl = template.Must(template.ParseFS(templateFS, "templates/status.html"))

// statusView — тексты и цвета статусов O07 по docs/ui/style-guide.md («Статусы O07 в интерфейсе»).
type statusView struct {
	Text string
	Tone string // info, success, success-muted, danger, neutral
}

var statuses = map[string]statusView{
	"PENDING":             {"Ожидает подтверждения", "info"},
	"CONFIRMED":           {"Подтверждено, единицы закреплены", "success"},
	"TRANSPORT_REQUESTED": {"Перевозка запрошена", "info"},
	"DISPATCHED":          {"Отправлено", "success"},
	"DELIVERED":           {"Доставлено", "success-muted"},
	"REJECTED":            {"Отклонено", "danger"},
	"EXPIRED":             {"Не подтверждено в срок", "neutral"},
	"CANCELLED":           {"Отменено", "neutral"},
	"BROKEN":              {"Нарушено: единица выбыла", "danger"},
}

// EventName — имя SSE-события обязательства: строка таблицы слушает его через sse-swap.
func EventName(id uuid.UUID) string { return "obligation-" + id.String() }

// renderStatus — фрагмент бейджа статуса. В нём только идентификатор обязательства и статус:
// ни номера случая, ни спецификации замены (ADR-0011).
func renderStatus(id uuid.UUID, status string) (string, error) {
	v, ok := statuses[status]
	if !ok {
		return "", fmt.Errorf("неизвестный статус %q", status)
	}
	var buf bytes.Buffer
	err := statusTmpl.Execute(&buf, map[string]string{
		"ID": id.String(), "Status": status, "Text": v.Text, "Tone": v.Tone,
	})
	if err != nil {
		return "", fmt.Errorf("фрагмент статуса: %w", err)
	}
	// SSE передаёт данные построчно; фрагмент держим в одной строке.
	return strings.Join(strings.Fields(buf.String()), " "), nil
}
