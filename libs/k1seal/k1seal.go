// Package k1seal — конверт с зашифрованным номером случая пациента (класс данных K1, ADR-0020).
//
// Номер случая — сведения о здоровье, специальная категория ПДн (152-ФЗ, ст. 10) и врачебная тайна
// (323-ФЗ, ст. 13). Открытый номер нужен только matching (заявка врача) и inventory (ИНТ-05, АИСТ-М);
// transfer, Kafka, outbox, аудит и журналы видят только конверт Sealed.
//
// Пакет не содержит ни ключей, ни криптографии: только тип Sealed, проверку формата и ошибки. Его импортирует
// кто угодно, в том числе libs/contracts и transfer. Связка ключей, шифрование и расшифровка — в
// libs/k1seal/opener, который tools/archtest разрешает (прямо и транзитивно) только matching и inventory.
package k1seal

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

const (
	// Version — версия формата конверта.
	Version = "v1"
	// EnvKeys — переменная окружения со связкой ключей: "kid:base64key,kid:base64key", первый — текущий.
	// Её получают только matching и inventory.
	EnvKeys = "BS_K1_KEYS"
	// MaxPlainLen — предел длины номера случая в байтах UTF-8 после обрезки пробелов. Номер короткий;
	// длинный текст означает, что в поле пытаются положить что-то другое.
	MaxPlainLen = 128
	// MaxKeyIDLen — предел длины идентификатора ключа.
	MaxKeyIDLen = 32
	// Overhead — nonce 12 байт и тег 16 байт AES-GCM в шифротексте конверта.
	Overhead = 12 + 16
	// maxEnvelopeLen ограничивает разбор конвертов из чужих сообщений.
	maxEnvelopeLen = 1024
	redacted       = "[скрыто]"
)

// Ошибки; проверяются через errors.Is. Тексты не содержат номеров, конвертов и ключей.
var (
	ErrKeyring    = errors.New("k1seal: неверная связка ключей")
	ErrInput      = errors.New("k1seal: недопустимые данные для шифрования")
	ErrMalformed  = errors.New("k1seal: неверный формат конверта")
	ErrUnknownKey = errors.New("k1seal: неизвестный ключ конверта")
	ErrOpen       = errors.New("k1seal: конверт не расшифрован: повреждён или относится к другой заявке")
)

// Sealed — конверт v1.<kid>.<base64url(nonce|ciphertext|tag)>. Значение — шифротекст: его можно хранить
// и передавать, MarshalJSON отдаёт его как есть. fmt и slog выводят только тип и kid.
//
// Ограничение: глаголы, которые fmt не может применить к строке через Formatter (%p, а также %w в Errorf
// с аргументом не-error), печатают значение рефлексией — это конверт (шифротекст), не номер. go vet такие
// вызовы ловит.
type Sealed string

// ParseSealed проверяет формат конверта без ключа, например при чтении из базы или сообщения.
func ParseSealed(s string) (Sealed, error) {
	if _, _, err := Sealed(s).Parts(); err != nil {
		return "", err
	}
	return Sealed(s), nil
}

// Compose собирает конверт из kid и шифротекста nonce|ciphertext|tag.
func Compose(kid string, ciphertext []byte) (Sealed, error) {
	if !ValidKeyID(kid) {
		return "", fmt.Errorf("%w: неверный идентификатор ключа", ErrMalformed)
	}
	return ParseSealed(Header(kid) + base64.RawURLEncoding.EncodeToString(ciphertext))
}

// Header — начало конверта "v1.<kid>."; входит в AAD, поэтому подмена версии или kid обнаруживается.
func Header(kid string) string { return Version + "." + kid + "." }

// ValidKeyID сообщает, что kid состоит из [A-Za-z0-9_-] и имеет длину 1..MaxKeyIDLen.
func ValidKeyID(kid string) bool {
	if kid == "" || len(kid) > MaxKeyIDLen {
		return false
	}
	for _, r := range kid {
		if !isURLSafe(r) {
			return false
		}
	}
	return true
}

// isURLSafe — символ алфавита base64url и идентификатора ключа: [A-Za-z0-9_-].
func isURLSafe(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
}

// Parts разбирает конверт на kid и шифротекст nonce|ciphertext|tag (без расшифровки).
func (s Sealed) Parts() (kid string, ciphertext []byte, err error) {
	raw := string(s)
	if raw == "" {
		return "", nil, fmt.Errorf("%w: пустой конверт", ErrMalformed)
	}
	if len(raw) > maxEnvelopeLen {
		return "", nil, fmt.Errorf("%w: длиннее %d символов", ErrMalformed, maxEnvelopeLen)
	}
	ver, rest, ok := strings.Cut(raw, ".")
	if !ok || ver != Version {
		return "", nil, fmt.Errorf("%w: ожидалась версия %s", ErrMalformed, Version)
	}
	kid, payload, ok := strings.Cut(rest, ".")
	if !ok || !ValidKeyID(kid) {
		return "", nil, fmt.Errorf("%w: неверный идентификатор ключа", ErrMalformed)
	}
	// Декодер base64 пропускает \r и \n, поэтому алфавит проверяется явно: Go-тип совпадает с pattern
	// схемы контрактов ^v1\.[A-Za-z0-9_-]{1,32}\.[A-Za-z0-9_-]+$.
	if payload == "" || strings.IndexFunc(payload, func(r rune) bool { return !isURLSafe(r) }) >= 0 {
		return "", nil, fmt.Errorf("%w: шифротекст не в base64url", ErrMalformed)
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(payload)
	if err != nil {
		return "", nil, fmt.Errorf("%w: шифротекст не в base64url", ErrMalformed)
	}
	if len(data) <= Overhead {
		return "", nil, fmt.Errorf("%w: шифротекст короче минимального", ErrMalformed)
	}
	return kid, data, nil
}

// IsZero сообщает, что конверт пуст.
func (s Sealed) IsZero() bool { return s == "" }

// KeyID возвращает идентификатор ключа конверта или пустую строку, если формат неверен.
func (s Sealed) KeyID() string {
	kid, _, err := s.Parts()
	if err != nil {
		return ""
	}
	return kid
}

// String не раскрывает содержимое конверта.
func (s Sealed) String() string {
	if s == "" {
		return "k1seal.Sealed(пусто)"
	}
	if kid := s.KeyID(); kid != "" {
		return "k1seal.Sealed(kid=" + kid + ")"
	}
	return "k1seal.Sealed(неверный формат)"
}

// GoString для %#v.
func (s Sealed) GoString() string { return s.String() }

// Format перехватывает глаголы fmt (%v, %s, %q, %x, %d…), чтобы конверт не печатался.
func (s Sealed) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, s.String()) }

// LogValue скрывает конверт в slog.
func (s Sealed) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON отдаёт конверт как есть: это шифротекст для сообщений и базы.
func (s Sealed) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(string(s))
	if err != nil {
		return nil, fmt.Errorf("k1seal: сериализация конверта: %w", err)
	}
	return b, nil
}

// UnmarshalJSON принимает только конверт верного формата; null и пустая строка дают пустой конверт.
func (s *Sealed) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("%w: ожидалась строка", ErrMalformed)
	}
	if raw == "" {
		*s = ""
		return nil
	}
	v, err := ParseSealed(raw)
	if err != nil {
		return err
	}
	*s = v
	return nil
}
