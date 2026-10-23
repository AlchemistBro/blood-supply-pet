// Package recipients — статический справочник адресатов уведомлений (ADR-0021): роль и клиника → адрес служебного
// ящика подразделения. Keycloak Admin API не используется: служебному клиенту понадобились бы права на чтение всех
// учётных записей realm с их личными адресами.
//
// Формат файла (BS_NOTIFY_RECIPIENTS_FILE; без него — встроенный demo.json):
//
//	{
//	  "version": 1,
//	  "duty": "duty-shift@demo.blood-supply.local",
//	  "operator_pattern": "transfusion.{clinic_code}@demo.blood-supply.local",
//	  "operators": {"1": "dispatch.c001@demo.blood-supply.local"}
//	}
//
// operators — явные ящики по clinic_id; operator_pattern — для остальных клиник, {clinic_code} подставляется
// строчными буквами из справочника клиник libs/refdata. Принимается только адрес без отображаемого имени.
package recipients

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"strconv"
	"strings"

	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/domain"
)

//go:embed demo.json
var demo []byte

const clinicCodeVar = "{clinic_code}"

type file struct {
	Version         int               `json:"version"`
	Note            string            `json:"note,omitempty"`
	Duty            string            `json:"duty"`
	OperatorPattern string            `json:"operator_pattern,omitempty"`
	Operators       map[string]string `json:"operators,omitempty"`
}

// Directory — загруженный справочник. Неизменяем после загрузки, безопасен для параллельного чтения.
type Directory struct {
	duty      string
	operators map[int64]string
	pattern   string
	codes     map[int64]string
}

// Default — встроенный демо-справочник.
func Default(clinics []refdata.Clinic) (*Directory, error) {
	return Load(bytes.NewReader(demo), clinics)
}

// LoadFile читает справочник из файла; пустой путь — встроенный демо-справочник.
func LoadFile(path string, clinics []refdata.Clinic) (*Directory, error) {
	if path == "" {
		return Default(clinics)
	}
	f, err := os.Open(path) //nolint:gosec // путь задаёт администратор в конфигурации сервиса
	if err != nil {
		return nil, fmt.Errorf("справочник адресатов: %w", err)
	}
	defer func() { _ = f.Close() }()
	return Load(f, clinics)
}

// Load разбирает и проверяет справочник. Неизвестные поля — ошибка: в справочник нельзя дописать ничего,
// кроме адресов ящиков.
func Load(r io.Reader, clinics []refdata.Clinic) (*Directory, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var f file
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("справочник адресатов: разбор: %w", err)
	}
	d := &Directory{operators: map[int64]string{}, codes: map[int64]string{}}
	for _, c := range clinics {
		d.codes[c.ClinicID] = strings.ToLower(c.ClinicCode)
	}
	if f.Duty == "" {
		return nil, errors.New("справочник адресатов: не задан ящик дежурной смены (duty)")
	}
	if err := checkAddress(f.Duty); err != nil {
		return nil, fmt.Errorf("справочник адресатов: duty: %w", err)
	}
	d.duty = f.Duty
	for key, addr := range f.Operators {
		id, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("справочник адресатов: ключ %q не номер клиники", key)
		}
		if _, ok := d.codes[id]; !ok {
			return nil, fmt.Errorf("справочник адресатов: клиники %d нет в справочнике клиник", id)
		}
		if err := checkAddress(addr); err != nil {
			return nil, fmt.Errorf("справочник адресатов: клиника %d: %w", id, err)
		}
		d.operators[id] = addr
	}
	if f.OperatorPattern != "" {
		if !strings.Contains(f.OperatorPattern, clinicCodeVar) {
			return nil, fmt.Errorf("справочник адресатов: operator_pattern без %s дал бы один ящик на все клиники", clinicCodeVar)
		}
		for id, code := range d.codes {
			if err := checkAddress(strings.ReplaceAll(f.OperatorPattern, clinicCodeVar, code)); err != nil {
				return nil, fmt.Errorf("справочник адресатов: operator_pattern для клиники %d: %w", id, err)
			}
		}
		d.pattern = f.OperatorPattern
	}
	return d, nil
}

// Address возвращает ящик адресата; false — адресат не настроен.
func (d *Directory) Address(r domain.Recipient) (string, bool) {
	switch r.Role {
	case domain.RoleDuty:
		return d.duty, d.duty != ""
	case domain.RoleOperator:
		if r.ClinicID == nil {
			return "", false
		}
		if addr, ok := d.operators[*r.ClinicID]; ok {
			return addr, true
		}
		code, ok := d.codes[*r.ClinicID]
		if !ok || d.pattern == "" {
			return "", false
		}
		return strings.ReplaceAll(d.pattern, clinicCodeVar, code), true
	default:
		return "", false
	}
}

// checkAddress пропускает только голый адрес (addr-spec): без имени и угловых скобок.
func checkAddress(s string) error {
	a, err := mail.ParseAddress(s)
	if err != nil {
		return fmt.Errorf("%q не адрес почты: %w", s, err)
	}
	if a.Name != "" || a.Address != s {
		return fmt.Errorf("%q: нужен только адрес ящика, без имени (ADR-0021)", s)
	}
	return nil
}
