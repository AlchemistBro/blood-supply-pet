package ui

import (
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

func TestFormatDateTimeInMoscow(t *testing.T) {
	utc := time.Date(2026, 10, 23, 11, 32, 59, 0, time.UTC)
	cases := []struct {
		name string
		fn   func(time.Time) string
		want string
	}{
		{"дата и время", FormatDateTime, "23.10.2026 14:32"},
		{"дата", FormatDate, "23.10.2026"},
		{"время", FormatTime, "14:32"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.fn(utc); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestFormatDateTimeCrossesMidnightInMoscow(t *testing.T) {
	// 22:30 UTC — уже следующие сутки по Москве.
	utc := time.Date(2026, 10, 23, 22, 30, 0, 0, time.UTC)
	if got := FormatDateTime(utc); got != "24.10.2026 01:30" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatZeroTimeIsDash(t *testing.T) {
	for _, fn := range []func(time.Time) string{FormatDateTime, FormatDate, FormatTime} {
		if got := fn(time.Time{}); got != "—" {
			t.Fatalf("got %q, want dash", got)
		}
	}
}

func TestFormatML(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 мл"},
		{300, "300 мл"},
		{1200, "1 200 мл"},
		{1234567, "1 234 567 мл"},
		{-450, "-450 мл"},
	}
	for _, c := range cases {
		if got := FormatML(c.in); got != c.want {
			t.Errorf("FormatML(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "меньше минуты"},
		{59 * time.Second, "меньше минуты"},
		{time.Minute, "1 мин"},
		{35 * time.Minute, "35 мин"},
		{2 * time.Hour, "2 ч"},
		{time.Hour + 12*time.Minute + 40*time.Second, "1 ч 12 мин"},
		{26 * time.Hour, "1 сут 2 ч"},
		{72 * time.Hour, "3 сут"},
		{-5 * time.Minute, "меньше минуты"},
	}
	for _, c := range cases {
		if got := FormatDuration(c.in); got != c.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Все статусы O07 из docs/domain/obligation-states.md имеют русское название и тон по style-guide.md.
func TestStatusBadgeCoversAllObligationStatuses(t *testing.T) {
	want := map[ObligationStatus]Badge{
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
	if len(AllObligationStatuses()) != len(want) {
		t.Fatalf("статусов %d, ожидалось %d", len(AllObligationStatuses()), len(want))
	}
	for _, s := range AllObligationStatuses() {
		got := StatusBadge(s)
		if got != want[s] {
			t.Errorf("StatusBadge(%s) = %+v, want %+v", s, got, want[s])
		}
	}
}

func TestStatusBadgeUnknownIsNeutralAndShowsCode(t *testing.T) {
	got := StatusBadge("SOMETHING_NEW")
	if got.Tone != ToneNeutral || got.Label != "Неизвестный статус (SOMETHING_NEW)" {
		t.Fatalf("got %+v", got)
	}
}

func TestAdditionalBadges(t *testing.T) {
	if got := PartialBadge(2, 3); got != (Badge{Label: "Частично: 2 из 3", Tone: ToneWarning}) {
		t.Errorf("PartialBadge = %+v", got)
	}
	if got := AistPendingBadge(); got != (Badge{Label: "Закрепление передаётся в АИСТ-М", Tone: ToneInfo, Spinner: true}) {
		t.Errorf("AistPendingBadge = %+v", got)
	}
	if got := AistAckBadge(); got != (Badge{Label: "Подтверждено АИСТ-М", Tone: ToneSuccess}) {
		t.Errorf("AistAckBadge = %+v", got)
	}
}

func TestRoleLabel(t *testing.T) {
	cases := map[auth.Role]string{
		auth.RolePhysician: "Врач-трансфузиолог",
		auth.RoleOperator:  "Оператор клинической организации",
		auth.RoleAnalyst:   "Территориальный аналитик",
		auth.RoleDuty:      "Дежурный смены",
		auth.RoleITService: "Внешняя ИТ-служба",
		auth.Role("X"):     "Роль не определена",
	}
	for role, want := range cases {
		if got := RoleLabel(role); got != want {
			t.Errorf("RoleLabel(%s) = %q, want %q", role, got, want)
		}
	}
}
