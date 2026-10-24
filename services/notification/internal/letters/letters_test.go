package letters_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/domain"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/letters"
)

// -update перезаписывает эталоны: go test ./services/notification/internal/letters -run Golden -update,
// затем просмотреть diff эталонов глазами.
var update = flag.Bool("update", false, "перезаписать эталоны testdata/*.golden")

var (
	obligationID = uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-300000000001")
	requestID    = uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-200000000001")
	deadline     = time.Date(2026, 10, 23, 10, 15, 0, 0, time.UTC)
)

func renderer(t *testing.T) *letters.Renderer {
	t.Helper()
	r, err := letters.New(refdata.MustLoad(), "http://localhost:8080/")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func projection(urgency string) *domain.Obligation {
	return &domain.Obligation{
		ObligationID: obligationID, RequestID: requestID, Origin: domain.OriginDoctorChoice,
		SourceClinicID: 1, TargetClinicID: 2, ComponentKind: "RBC", RequestedQty: 3, Urgency: urgency, Deadline: deadline,
	}
}

// Три вида писем: срочное задание со срочностью, эскалация по проекции без срочности, нарушение без проекции.
func drafts(t *testing.T) map[string]domain.Draft {
	t.Helper()
	urgent, ok := domain.OnObligationOpened(*projection(domain.UrgencyUrgent))
	if !ok {
		t.Fatal("срочное задание по выбору врача не дало письма")
	}
	return map[string]domain.Draft{
		"urgent_task":     urgent,
		"escalation":      domain.OnEscalation(obligationID, requestID, "EXPIRED_NO_FALLBACK", projection("")),
		"transfer_broken": domain.OnTransferBroken(obligationID, requestID, "QUARANTINE", nil),
	}
}

func TestLettersMatchGolden(t *testing.T) {
	r := renderer(t)
	for name, d := range drafts(t) {
		l, err := r.Render(d.Kind, d.Facts)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		golden(t, name+".txt", l.Text)
		golden(t, name+".html", l.HTML)
	}
}

func TestSubjects(t *testing.T) {
	r := renderer(t)
	want := map[string]string{
		"urgent_task":     "[демо] Срочное задание на передачу № 0192f3a1",
		"escalation":      "[демо] Эскалация по заданию № 0192f3a1",
		"transfer_broken": "[демо] Нарушено обязательство на передачу № 0192f3a1",
	}
	for name, d := range drafts(t) {
		if got := r.Subject(d.Kind, d.Facts); got != want[name] {
			t.Errorf("%s: тема %q, ожидалась %q", name, got, want[name])
		}
		l, err := r.Render(d.Kind, d.Facts)
		if err != nil {
			t.Fatal(err)
		}
		if l.Subject != want[name] {
			t.Errorf("%s: тема письма %q расходится с Subject %q", name, l.Subject, want[name])
		}
	}
}

// В письмах нет фенотипа, номера случая пациента, ФИО (FR044, NFR016, ADR-0011), а пометка «демо» есть.
func TestLettersCarryNoPersonalOrClinicalSpecifics(t *testing.T) {
	forbidden := []string{"фенотип", "phenotype", "номер случая", "patient_case", "ФИО", "СНИЛС", "snils", "full_name", "пациент"}
	r := renderer(t)
	for name, d := range drafts(t) {
		l, err := r.Render(d.Kind, d.Facts)
		if err != nil {
			t.Fatal(err)
		}
		for part, body := range map[string]string{"тема": l.Subject, "text": l.Text, "html": l.HTML} {
			low := strings.ToLower(body)
			for _, f := range forbidden {
				if strings.Contains(low, strings.ToLower(f)) {
					t.Errorf("%s (%s) содержит %q", name, part, f)
				}
			}
			if !strings.Contains(low, "демо") {
				t.Errorf("%s (%s) без пометки «демо»", name, part)
			}
		}
		if !strings.Contains(l.Text, obligationID.String()) || !strings.Contains(l.HTML, obligationID.String()) {
			t.Errorf("%s: в письме нет номера задания", name)
		}
	}
}

// Ссылка ведёт на экран web: оператору — карточка задания, дежурному — эскалации.
func TestLinksPointToWebScreens(t *testing.T) {
	r := renderer(t)
	d := drafts(t)
	cases := map[string]string{
		"urgent_task":     "http://localhost:8080/tasks/" + obligationID.String(),
		"escalation":      "http://localhost:8080/duty/escalations",
		"transfer_broken": "http://localhost:8080/duty/tasks",
	}
	for name, link := range cases {
		l, err := r.Render(d[name].Kind, d[name].Facts)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(l.Text, link) || !strings.Contains(l.HTML, `href="`+link+`"`) {
			t.Errorf("%s: нет ссылки %s", name, link)
		}
	}
}

func TestNewRejectsBadBaseURL(t *testing.T) {
	for _, base := range []string{"", "localhost:8080", "javascript:alert(1)", "ftp://host"} {
		if _, err := letters.New(refdata.MustLoad(), base); err == nil {
			t.Errorf("адрес web %q принят, ожидалась ошибка", base)
		}
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // путь к эталону внутри testdata
	if err != nil {
		t.Fatalf("эталон %s: %v (создать: -update)", path, err)
	}
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if got != string(want) {
		t.Errorf("%s расходится с эталоном\n--- получено ---\n%s\n--- эталон ---\n%s", name, got, want)
	}
}
