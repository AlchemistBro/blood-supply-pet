package recipients_test

import (
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/domain"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/recipients"
)

func clinic(id int64) *int64 { return &id }

func clinics() []refdata.Clinic { return refdata.MustLoad().Clinics }

// Встроенный демо-справочник покрывает дежурного и операторов всех клиник шаблоном.
func TestDefaultDirectoryCoversDutyAndEveryClinic(t *testing.T) {
	d, err := recipients.Default(clinics())
	if err != nil {
		t.Fatal(err)
	}
	if addr, ok := d.Address(domain.Recipient{Role: domain.RoleDuty}); !ok || addr == "" {
		t.Fatalf("дежурный без адреса: %q", addr)
	}
	for _, c := range clinics() {
		addr, ok := d.Address(domain.Recipient{Role: domain.RoleOperator, ClinicID: clinic(c.ClinicID)})
		if !ok {
			t.Fatalf("клиника %s без адреса оператора", c.ClinicCode)
		}
		if !strings.Contains(addr, strings.ToLower(c.ClinicCode)) {
			t.Fatalf("адрес %q не содержит кода клиники %s", addr, c.ClinicCode)
		}
	}
}

// Явная запись важнее шаблона; клиника без записи и без шаблона адреса не имеет.
func TestExplicitOperatorOverridesPattern(t *testing.T) {
	src := `{"version":1,"duty":"duty-shift@example.org","operator_pattern":"transfusion.{clinic_code}@example.org",
		"operators":{"1":"blood-center.dispatch@example.org"}}`
	d, err := recipients.Load(strings.NewReader(src), clinics())
	if err != nil {
		t.Fatal(err)
	}
	if addr, _ := d.Address(domain.Recipient{Role: domain.RoleOperator, ClinicID: clinic(1)}); addr != "blood-center.dispatch@example.org" {
		t.Fatalf("клиника 1: %q, ожидалась явная запись", addr)
	}
	if addr, _ := d.Address(domain.Recipient{Role: domain.RoleOperator, ClinicID: clinic(2)}); addr != "transfusion.c002@example.org" {
		t.Fatalf("клиника 2: %q, ожидался адрес по шаблону", addr)
	}

	noPattern, err := recipients.Load(strings.NewReader(`{"version":1,"duty":"duty@example.org","operators":{"1":"c1@example.org"}}`), clinics())
	if err != nil {
		t.Fatal(err)
	}
	if addr, ok := noPattern.Address(domain.Recipient{Role: domain.RoleOperator, ClinicID: clinic(2)}); ok {
		t.Fatalf("клиника 2 без записи и шаблона получила адрес %q", addr)
	}
	if _, ok := noPattern.Address(domain.Recipient{Role: domain.RoleOperator}); ok {
		t.Fatal("оператор без клиники не может иметь адреса")
	}
	if _, ok := noPattern.Address(domain.Recipient{Role: "PHYSICIAN"}); ok {
		t.Fatal("роль вне справочника не может иметь адреса")
	}
}

// Справочник принимает только адрес ящика: имя человека в заголовок To не попадает (ADR-0021).
func TestDirectoryRejectsPersonalOrBrokenEntries(t *testing.T) {
	cases := map[string]string{
		"отображаемое имя":        `{"version":1,"duty":"Иванов Иван <ivanov@example.org>"}`,
		"угловые скобки":          `{"version":1,"duty":"<duty@example.org>"}`,
		"не адрес":                `{"version":1,"duty":"дежурный"}`,
		"нет дежурного":           `{"version":1,"operators":{"1":"c1@example.org"}}`,
		"неизвестная клиника":     `{"version":1,"duty":"duty@example.org","operators":{"999999":"x@example.org"}}`,
		"ключ не номер клиники":   `{"version":1,"duty":"duty@example.org","operators":{"C001":"x@example.org"}}`,
		"лишнее поле":             `{"version":1,"duty":"duty@example.org","full_name":"Иванов"}`,
		"шаблон без кода клиники": `{"version":1,"duty":"duty@example.org","operator_pattern":"all@example.org"}`,
		"шаблон даёт не адрес":    `{"version":1,"duty":"duty@example.org","operator_pattern":"Оператор {clinic_code}"}`,
	}
	for name, src := range cases {
		if _, err := recipients.Load(strings.NewReader(src), clinics()); err == nil {
			t.Errorf("%s: справочник принят, ожидалась ошибка", name)
		}
	}
}
