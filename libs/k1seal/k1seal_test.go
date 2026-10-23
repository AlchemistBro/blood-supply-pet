package k1seal_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/k1seal"
	"github.com/AlchemistBro/blood-supply-pet/libs/k1seal/opener"
)

// Демо-ключи для тестов, не для эксплуатации.
func key(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

const (
	plainCase = "ИБ-2026-000417"
	requestID = "0192f0c1-7a2b-7c3d-8e4f-5a6b7c8d9e0f"
)

func mustRing(t *testing.T, spec string) *opener.Keyring {
	t.Helper()
	kr, err := opener.ParseKeyring(spec)
	if err != nil {
		t.Fatalf("ParseKeyring: %v", err)
	}
	return kr
}

func TestParseKeyringValid(t *testing.T) {
	kr := mustRing(t, " new:"+key(2)+" , old:"+key(1)+" ")
	if got := kr.CurrentKeyID(); got != "new" {
		t.Fatalf("текущий ключ %q, ожидался new", got)
	}
	if got := strings.Join(kr.KeyIDs(), ","); got != "new,old" {
		t.Fatalf("ключи %q, ожидались new,old", got)
	}
}

func TestParseKeyringRejects(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 16))
	cases := map[string]string{
		"пустая строка":     "",
		"только пробелы":    "  ,  ",
		"нет двоеточия":     key(1),
		"пустой kid":        ":" + key(1),
		"kid с точкой":      "a.b:" + key(1),
		"kid длиннее 32":    strings.Repeat("k", 33) + ":" + key(1),
		"не base64":         "k1:не-base64!!",
		"ключ 16 байт":      "k1:" + short,
		"повтор kid":        "k1:" + key(1) + ",k1:" + key(2),
		"пустой ключ":       "k1:",
		"лишняя запятая":    "k1:" + key(1) + ",,k2:" + key(2),
		"ключ 33 байта":     "k1:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 33)),
		"base64url вместо":  "k1:" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32)),
		"пробел внутри kid": "k 1:" + key(1),
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := opener.ParseKeyring(spec)
			if !errors.Is(err, k1seal.ErrKeyring) {
				t.Fatalf("ожидалась ErrKeyring, получено %v", err)
			}
			// Текст ошибки не содержит материала ключей.
			for _, b := range []byte{1, 2, 3, 7} {
				if strings.Contains(err.Error(), key(b)) {
					t.Fatalf("ошибка раскрывает ключ: %v", err)
				}
			}
			if strings.Contains(err.Error(), short) {
				t.Fatalf("ошибка раскрывает ключ: %v", err)
			}
		})
	}
}

func TestKeyringFromEnv(t *testing.T) {
	t.Setenv(k1seal.EnvKeys, "demo:"+key(5))
	kr, err := opener.KeyringFromEnv()
	if err != nil {
		t.Fatalf("KeyringFromEnv: %v", err)
	}
	if kr.CurrentKeyID() != "demo" {
		t.Fatalf("текущий ключ %q", kr.CurrentKeyID())
	}
	t.Setenv(k1seal.EnvKeys, "")
	if _, err := opener.KeyringFromEnv(); !errors.Is(err, k1seal.ErrKeyring) {
		t.Fatalf("пустая переменная: ожидалась ErrKeyring, получено %v", err)
	}
}

var envelopeRe = regexp.MustCompile(`^v1\.cur\.[A-Za-z0-9_-]+$`)

func TestSealEnvelopeFormat(t *testing.T) {
	kr := mustRing(t, "cur:"+key(1)+",old:"+key(2))
	s, err := kr.Seal(plainCase, requestID)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !envelopeRe.MatchString(string(s)) {
		t.Fatalf("конверт %q не соответствует v1.<kid>.<base64url>", string(s))
	}
	if s.KeyID() != "cur" {
		t.Fatalf("KeyID %q", s.KeyID())
	}
	if strings.Contains(string(s), plainCase) {
		t.Fatal("конверт содержит открытый текст")
	}
	if _, err := k1seal.ParseSealed(string(s)); err != nil {
		t.Fatalf("ParseSealed собственного конверта: %v", err)
	}
}

func TestSealTwiceDiffers(t *testing.T) {
	kr := mustRing(t, "cur:"+key(1))
	a, err := kr.Seal(plainCase, requestID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := kr.Seal(plainCase, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("два шифрования одного номера дали одинаковые конверты")
	}
}

func TestSealRejectsBadInput(t *testing.T) {
	kr := mustRing(t, "cur:"+key(1))
	cases := map[string][2]string{
		"пустой номер":          {"", requestID},
		"номер из пробелов":     {" \t\n ", requestID},
		"пустая заявка":         {plainCase, ""},
		"слишком длинный номер": {strings.Repeat("9", 129), requestID},
		"номер не в UTF-8":      {"ИБ-\xff\xfe-17", requestID},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := kr.Seal(c[0], c[1])
			if !errors.Is(err, k1seal.ErrInput) {
				t.Fatalf("ожидалась ErrInput, получено %v", err)
			}
			if c[0] != "" && strings.TrimSpace(c[0]) != "" && strings.Contains(err.Error(), c[0]) {
				t.Fatalf("ошибка содержит открытый текст: %v", err)
			}
		})
	}
	var nilRing *opener.Keyring
	if _, err := nilRing.Seal(plainCase, requestID); !errors.Is(err, k1seal.ErrKeyring) {
		t.Fatalf("nil-связка: ожидалась ErrKeyring, получено %v", err)
	}
}

func TestParseSealedRejectsMalformed(t *testing.T) {
	kr := mustRing(t, "cur:"+key(1))
	good, err := kr.Seal(plainCase, requestID)
	if err != nil {
		t.Fatal(err)
	}
	_, payload, _ := strings.Cut(strings.TrimPrefix(string(good), "v1."), ".")
	cases := map[string]string{
		"пусто":               "",
		"без частей":          "garbage",
		"другая версия":       "v2.cur." + payload,
		"пустой kid":          "v1.." + payload,
		"лишняя точка":        "v1.cur." + payload + ".x",
		"не base64url":        "v1.cur.+/+/",
		"короткий шифротекст": "v1.cur." + base64.RawURLEncoding.EncodeToString(make([]byte, 28)),
		"слишком длинный":     "v1.cur." + strings.Repeat("A", 2048),
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := k1seal.ParseSealed(s); !errors.Is(err, k1seal.ErrMalformed) {
				t.Fatalf("ожидалась ErrMalformed, получено %v", err)
			}
		})
	}
}

// Вывод fmt и slog не раскрывает ни номер, ни конверт: только тип и kid.
func TestSealedDoesNotLeak(t *testing.T) {
	kr := mustRing(t, "cur:"+key(1))
	s, err := kr.Seal(plainCase, requestID)
	if err != nil {
		t.Fatal(err)
	}
	_, payload, _ := strings.Cut(strings.TrimPrefix(string(s), "v1."), ".")
	leaks := func(out string) bool {
		return strings.Contains(out, plainCase) || strings.Contains(out, payload) || strings.Contains(out, string(s))
	}

	type holder struct{ Case k1seal.Sealed }
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%10.3s"} {
		out := fmt.Sprintf(verb, s)
		if leaks(out) {
			t.Errorf("fmt %s раскрывает конверт: %q", verb, out)
		}
		if !strings.Contains(out, "cur") {
			t.Errorf("fmt %s не показывает kid: %q", verb, out)
		}
		if out := fmt.Sprintf(verb, holder{s}); leaks(out) {
			t.Errorf("fmt %s структуры раскрывает конверт: %q", verb, out)
		}
		if out := fmt.Sprintf(verb, &s); leaks(out) {
			t.Errorf("fmt %s указателя раскрывает конверт: %q", verb, out)
		}
	}
	if out := fmt.Sprint(s); leaks(out) {
		t.Errorf("Sprint раскрывает конверт: %q", out)
	}
	// %p к строке не применим: fmt печатает значение рефлексией, минуя Format, и выводит конверт (шифротекст).
	// Это известное ограничение (go vet ловит такие вызовы); открытый номер не утекает и здесь.
	pVerb := "%p"
	if out := fmt.Sprintf(pVerb, s); strings.Contains(out, plainCase) {
		t.Errorf("fmt %%p раскрывает номер: %q", out)
	}
	if out := s.String(); leaks(out) {
		t.Errorf("String раскрывает конверт: %q", out)
	}

	var buf bytes.Buffer
	jsonLog := slog.New(slog.NewJSONHandler(&buf, nil))
	jsonLog.Info("заявка", "patient_case", s, slog.Any("sealed", s), slog.Group("g", "v", s))
	textLog := slog.New(slog.NewTextHandler(&buf, nil))
	textLog.Info("заявка", "patient_case", s, slog.Any("sealed", s))
	if leaks(buf.String()) {
		t.Fatalf("slog раскрывает конверт: %s", buf.String())
	}
	if c := strings.Count(buf.String(), "[скрыто]"); c != 5 {
		t.Fatalf("ожидалось 5 замен на [скрыто], найдено %d: %s", c, buf.String())
	}
}

func TestSealedStringForEmptyAndMalformed(t *testing.T) {
	if out := fmt.Sprint(k1seal.Sealed("")); strings.Contains(out, "v1") {
		t.Fatalf("пустой конверт: %q", out)
	}
	bad := k1seal.Sealed("номер-в-открытую-12345")
	if out := fmt.Sprintf("%v %s %#v", bad, bad, bad); strings.Contains(out, "12345") {
		t.Fatalf("неверный конверт печатается как есть: %q", out)
	}
	if bad.KeyID() != "" {
		t.Fatalf("KeyID неверного конверта %q", bad.KeyID())
	}
}

func TestSealedJSON(t *testing.T) {
	kr := mustRing(t, "cur:"+key(1))
	s, err := kr.Seal(plainCase, requestID)
	if err != nil {
		t.Fatal(err)
	}
	type msg struct {
		RequestID string        `json:"request_id"`
		Case      k1seal.Sealed `json:"patient_case_sealed"`
	}
	raw, err := json.Marshal(msg{RequestID: requestID, Case: s})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"patient_case_sealed":"`+string(s)+`"`)) {
		t.Fatalf("JSON должен нести конверт как есть: %s", raw)
	}
	if bytes.Contains(raw, []byte(plainCase)) {
		t.Fatalf("JSON содержит открытый текст: %s", raw)
	}
	var back msg
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.Case != s {
		t.Fatal("конверт изменился после JSON")
	}
	var bad msg
	err = json.Unmarshal([]byte(`{"patient_case_sealed":"ИБ-2026-000417"}`), &bad)
	if !errors.Is(err, k1seal.ErrMalformed) {
		t.Fatalf("открытый номер в JSON: ожидалась ErrMalformed, получено %v", err)
	}
	if strings.Contains(err.Error(), plainCase) {
		t.Fatalf("ошибка содержит открытый текст: %v", err)
	}
	var empty msg
	if err := json.Unmarshal([]byte(`{"patient_case_sealed":null}`), &empty); err != nil || !empty.Case.IsZero() {
		t.Fatalf("null: err=%v, zero=%v", err, empty.Case.IsZero())
	}
}

func TestKeyringDoesNotLeak(t *testing.T) {
	kr := mustRing(t, "cur:"+key(9)+",old:"+key(8))
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("старт", "keyring", kr)
	outs := []string{
		fmt.Sprintf("%v", kr), fmt.Sprintf("%+v", kr), fmt.Sprintf("%#v", kr), fmt.Sprintf("%s", kr),
		fmt.Sprintf("%v", *kr), fmt.Sprintf("%+v", *kr), fmt.Sprintf("%#v", *kr), buf.String(),
	}
	raw9, raw8 := string(bytes.Repeat([]byte{9}, 32)), string(bytes.Repeat([]byte{8}, 32))
	for _, out := range outs {
		if strings.Contains(out, key(9)) || strings.Contains(out, key(8)) || strings.Contains(out, raw9) || strings.Contains(out, raw8) {
			t.Fatalf("вывод связки раскрывает ключ: %q", out)
		}
	}
}

// FuzzParseSealed: разбор любого входа не паникует; принятый конверт проходит JSON туда и обратно,
// а его вывод в fmt не содержит шифротекста.
func FuzzParseSealed(f *testing.F) {
	kr, err := opener.ParseKeyring("cur:" + key(1))
	if err != nil {
		f.Fatal(err)
	}
	good, err := kr.Seal(plainCase, requestID)
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{"", "v1", "v1.", "v1..", "v1.cur.", "v1.cur.AAAA", string(good), string(good) + ".", "v2" + string(good)[2:], plainCase, "v1.\xff.\x00"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		s, err := k1seal.ParseSealed(in)
		_ = k1seal.Sealed(in).String()
		_ = k1seal.Sealed(in).KeyID()
		if err != nil {
			if !errors.Is(err, k1seal.ErrMalformed) {
				t.Fatalf("ошибка не ErrMalformed: %v", err)
			}
			return
		}
		if s.KeyID() == "" {
			t.Fatalf("принятый конверт без kid")
		}
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var back k1seal.Sealed
		if err := json.Unmarshal(raw, &back); err != nil || back != s {
			t.Fatalf("JSON туда и обратно: %v", err)
		}
		_, payload, _ := strings.Cut(strings.TrimPrefix(in, "v1."), ".")
		if out := fmt.Sprintf("%v", s); strings.Contains(out, payload) {
			t.Fatalf("fmt раскрывает шифротекст: %q", out)
		}
	})
}
