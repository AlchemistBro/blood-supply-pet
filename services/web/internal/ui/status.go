package ui

import (
	"strconv"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

// Tone — смысловой цвет бейджа, уведомления или полосы (палитра style-guide.md).
type Tone string

// Тоны интерфейса.
const (
	ToneInfo         Tone = "info"
	ToneSuccess      Tone = "success"
	ToneSuccessMuted Tone = "success-muted"
	ToneWarning      Tone = "warning"
	ToneDanger       Tone = "danger"
	ToneNeutral      Tone = "neutral"
)

// Badge — бейдж статуса: текст обязателен, цвет только дополняет его (доступность: статус не только цветом).
type Badge struct {
	Label   string
	Tone    Tone
	Spinner bool // идёт процесс (передача закрепления в АИСТ-М)
}

// ObligationStatus — статус обязательства на передачу O07 в том виде, в каком его отдаёт transfer.
// Список — только из docs/domain/obligation-states.md.
type ObligationStatus string

// Статусы O07.
const (
	StatusPending            ObligationStatus = "PENDING"
	StatusConfirmed          ObligationStatus = "CONFIRMED"
	StatusTransportRequested ObligationStatus = "TRANSPORT_REQUESTED"
	StatusDispatched         ObligationStatus = "DISPATCHED"
	StatusDelivered          ObligationStatus = "DELIVERED"
	StatusRejected           ObligationStatus = "REJECTED"
	StatusExpired            ObligationStatus = "EXPIRED"
	StatusCancelled          ObligationStatus = "CANCELLED"
	StatusBroken             ObligationStatus = "BROKEN"
)

// statusBadges — тексты и цвета из таблицы «Статусы O07 в интерфейсе» style-guide.md.
var statusBadges = map[ObligationStatus]Badge{
	StatusPending:            {Label: "Ожидает подтверждения", Tone: ToneInfo},
	StatusConfirmed:          {Label: "Подтверждено, единицы закреплены", Tone: ToneSuccess},
	StatusTransportRequested: {Label: "Перевозка запрошена", Tone: ToneInfo},
	StatusDispatched:         {Label: "Отправлено", Tone: ToneSuccess},
	StatusDelivered:          {Label: "Доставлено", Tone: ToneSuccessMuted},
	StatusRejected:           {Label: "Отклонено", Tone: ToneDanger},
	StatusExpired:            {Label: "Не подтверждено в срок", Tone: ToneNeutral},
	StatusCancelled:          {Label: "Отменено", Tone: ToneNeutral},
	StatusBroken:             {Label: "Нарушено: единица выбыла", Tone: ToneDanger},
}

// AllObligationStatuses — все статусы O07 в порядке жизненного цикла (для фильтров и тестов).
func AllObligationStatuses() []ObligationStatus {
	return []ObligationStatus{
		StatusPending, StatusConfirmed, StatusTransportRequested, StatusDispatched, StatusDelivered,
		StatusRejected, StatusExpired, StatusCancelled, StatusBroken,
	}
}

// StatusBadge — бейдж статуса O07. Неизвестный код (новая версия transfer раньше web) показывается
// нейтрально с кодом, а не теряется.
func StatusBadge(s ObligationStatus) Badge {
	if b, ok := statusBadges[s]; ok {
		return b
	}
	return Badge{Label: "Неизвестный статус (" + string(s) + ")", Tone: ToneNeutral}
}

// StatusLabel — человеческое название статуса O07.
func StatusLabel(s ObligationStatus) string {
	return StatusBadge(s).Label
}

// PartialBadge — признак частичного подтверждения: «Частично: 2 из 3». Это не статус O07,
// а confirmed_qty < requested_qty (obligation-states.md).
func PartialBadge(confirmed, requested int) Badge {
	return Badge{
		Label: "Частично: " + strconv.Itoa(confirmed) + " из " + strconv.Itoa(requested),
		Tone:  ToneWarning,
	}
}

// AistPendingBadge — закрепление ещё не подтверждено АИСТ-М (FR051, FR052): O07 в CONFIRMED, квитанции нет.
func AistPendingBadge() Badge {
	return Badge{Label: "Закрепление передаётся в АИСТ-М", Tone: ToneInfo, Spinner: true}
}

// AistAckBadge — квитанция АИСТ-М о закреплении получена.
func AistAckBadge() Badge {
	return Badge{Label: "Подтверждено АИСТ-М", Tone: ToneSuccess}
}

// roleLabels — названия ролей из глоссария (docs/domain/glossary.md, «Роли»).
var roleLabels = map[auth.Role]string{
	auth.RolePhysician: "Врач-трансфузиолог",
	auth.RoleOperator:  "Оператор клинической организации",
	auth.RoleAnalyst:   "Территориальный аналитик",
	auth.RoleDuty:      "Дежурный смены",
	auth.RoleITService: "Внешняя ИТ-служба",
}

// RoleLabel — русское название роли для шапки.
func RoleLabel(r auth.Role) string {
	if l, ok := roleLabels[r]; ok {
		return l
	}
	return "Роль не определена"
}
