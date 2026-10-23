// Package envelope — криптография конверта номера случая (ADR-0020): AES-256-GCM, конверт
// v1.<kid>.<base64url(nonce|ciphertext|tag)>, AAD = заголовок конверта + request_id.
// Пакет внутренний: расшифровку снаружи даёт только libs/k1seal/opener, импорт которого
// ограничен tools/archtest. Публичные имена переэкспортирует libs/k1seal.
package envelope

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"unicode/utf8"
)

const (
	version = "v1"
	keySize = 32
	// overhead — nonce 12 байт и тег 16 байт GCM.
	overhead = 12 + 16
	// MaxPlainLen — предел длины номера случая в байтах UTF-8. Номер короткий; длинный текст
	// означает, что в поле пытаются положить что-то другое.
	MaxPlainLen = 128
	// maxEnvelopeLen ограничивает разбор конвертов из чужих сообщений.
	maxEnvelopeLen = 1024
	maxKeyIDLen    = 32
	redacted       = "[скрыто]"
)

// Ошибки. Тексты не содержат ни открытого номера, ни конверта, ни ключей.
var (
	ErrKeyring    = errors.New("k1seal: неверная связка ключей")
	ErrInput      = errors.New("k1seal: недопустимые данные для шифрования")
	ErrMalformed  = errors.New("k1seal: неверный формат конверта")
	ErrUnknownKey = errors.New("k1seal: неизвестный ключ конверта")
	ErrOpen       = errors.New("k1seal: конверт не расшифрован: повреждён или относится к другой заявке")
)

// Sealed — конверт с зашифрованным номером случая. Значение — шифротекст: его можно хранить и передавать,
// но в журналы и fmt он не выводится, видны только тип и kid.
type Sealed string

// IsZero сообщает, что конверт пуст.
func (s Sealed) IsZero() bool { return s == "" }

// KeyID возвращает идентификатор ключа конверта или пустую строку, если формат неверен.
func (s Sealed) KeyID() string {
	kid, _, err := split(string(s))
	if err != nil {
		return ""
	}
	return kid
}

// String не раскрывает содержимое конверта.
func (s Sealed) String() string {
	switch {
	case s == "":
		return "k1seal.Sealed(пусто)"
	case s.KeyID() == "":
		return "k1seal.Sealed(неверный формат)"
	default:
		return "k1seal.Sealed(kid=" + s.KeyID() + ")"
	}
}

// GoString для %#v.
func (s Sealed) GoString() string { return s.String() }

// Format перехватывает все глаголы fmt (%v, %s, %q, %x, %d…), чтобы конверт не печатался ни при каком формате.
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
	v, err := Parse(raw)
	if err != nil {
		return err
	}
	*s = v
	return nil
}

// Parse проверяет формат конверта без ключа.
func Parse(raw string) (Sealed, error) {
	if _, _, err := split(raw); err != nil {
		return "", err
	}
	return Sealed(raw), nil
}

// split разбирает конверт на kid и полезную нагрузку nonce|ciphertext|tag.
func split(raw string) (string, []byte, error) {
	if raw == "" {
		return "", nil, fmt.Errorf("%w: пустой конверт", ErrMalformed)
	}
	if len(raw) > maxEnvelopeLen {
		return "", nil, fmt.Errorf("%w: длиннее %d символов", ErrMalformed, maxEnvelopeLen)
	}
	ver, rest, ok := strings.Cut(raw, ".")
	if !ok || ver != version {
		return "", nil, fmt.Errorf("%w: ожидалась версия %s", ErrMalformed, version)
	}
	kid, payload, ok := strings.Cut(rest, ".")
	if !ok || !validKeyID(kid) {
		return "", nil, fmt.Errorf("%w: неверный идентификатор ключа", ErrMalformed)
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(payload)
	if err != nil {
		return "", nil, fmt.Errorf("%w: шифротекст не в base64url", ErrMalformed)
	}
	if len(data) <= overhead {
		return "", nil, fmt.Errorf("%w: шифротекст короче минимального", ErrMalformed)
	}
	return kid, data, nil
}

func validKeyID(kid string) bool {
	if kid == "" || len(kid) > maxKeyIDLen {
		return false
	}
	for _, r := range kid {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

// header — начало конверта; входит в AAD, поэтому подмена версии или kid обнаруживается.
func header(kid string) string { return version + "." + kid + "." }

func aad(kid, requestID string) []byte { return []byte(header(kid) + requestID) }

// Ring — связка ключей: первый шифрует, все расшифровывают. Состояние за указателем, чтобы печать
// значения Ring через рефлексию fmt не выводила внутренности AES.
type Ring struct{ st *ringState }

type ringState struct {
	current string
	kids    []string
	aeads   map[string]cipher.AEAD
}

// ParseRing разбирает строку вида "kid:base64key,kid:base64key". Ключ — 32 байта в стандартном base64.
// Ошибки указывают номер записи и не содержат ключей.
func ParseRing(spec string) (*Ring, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, fmt.Errorf("%w: список ключей пуст", ErrKeyring)
	}
	st := &ringState{aeads: map[string]cipher.AEAD{}}
	for i, entry := range strings.Split(spec, ",") {
		n := i + 1
		kid, enc, ok := strings.Cut(strings.TrimSpace(entry), ":")
		if !ok {
			return nil, fmt.Errorf("%w: запись %d не в формате kid:base64key", ErrKeyring, n)
		}
		kid, enc = strings.TrimSpace(kid), strings.TrimSpace(enc)
		if !validKeyID(kid) {
			return nil, fmt.Errorf("%w: запись %d: kid должен состоять из [A-Za-z0-9_-], 1..%d символов", ErrKeyring, n, maxKeyIDLen)
		}
		if _, dup := st.aeads[kid]; dup {
			return nil, fmt.Errorf("%w: kid %q повторяется", ErrKeyring, kid)
		}
		key, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return nil, fmt.Errorf("%w: ключ %q не в стандартном base64", ErrKeyring, kid)
		}
		if len(key) != keySize {
			clear(key)
			return nil, fmt.Errorf("%w: ключ %q длиной %d байт, нужно %d (AES-256)", ErrKeyring, kid, len(key), keySize)
		}
		aead, err := newAEAD(key)
		clear(key)
		if err != nil {
			return nil, fmt.Errorf("%w: ключ %q: %w", ErrKeyring, kid, err)
		}
		st.aeads[kid] = aead
		st.kids = append(st.kids, kid)
	}
	st.current = st.kids[0]
	return &Ring{st: st}, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("AES: %w", err)
	}
	// nonce 96 бит из crypto/rand на каждое шифрование, префиксом к шифротексту.
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, fmt.Errorf("GCM: %w", err)
	}
	return aead, nil
}

func (r *Ring) usable() bool { return r != nil && r.st != nil && len(r.st.aeads) > 0 }

// CurrentKeyID — kid ключа, которым шифруются новые конверты.
func (r *Ring) CurrentKeyID() string {
	if !r.usable() {
		return ""
	}
	return r.st.current
}

// KeyIDs — все kid связки в порядке задания, первый — текущий.
func (r *Ring) KeyIDs() []string {
	if !r.usable() {
		return nil
	}
	return append([]string(nil), r.st.kids...)
}

// Seal нормализует номер случая (обрезка пробелов) и шифрует его текущим ключом с привязкой к заявке.
func (r *Ring) Seal(plain, requestID string) (Sealed, error) {
	if !r.usable() {
		return "", fmt.Errorf("%w: связка не задана", ErrKeyring)
	}
	plain = strings.TrimSpace(plain)
	switch {
	case plain == "":
		return "", fmt.Errorf("%w: номер случая пуст", ErrInput)
	case len(plain) > MaxPlainLen:
		return "", fmt.Errorf("%w: номер случая длиннее %d байт", ErrInput, MaxPlainLen)
	case !utf8.ValidString(plain):
		return "", fmt.Errorf("%w: номер случая не в UTF-8", ErrInput)
	case requestID == "":
		return "", fmt.Errorf("%w: не задан идентификатор заявки", ErrInput)
	}
	kid := r.st.current
	// nonce = nil обязателен для NewGCMWithRandomNonce: случайный nonce создаёт сам AEAD.
	ct := r.st.aeads[kid].Seal(nil, nil, []byte(plain), aad(kid, requestID)) //nolint:gosec // G407 — ложное срабатывание, см. выше

	return Sealed(header(kid) + base64.RawURLEncoding.EncodeToString(ct)), nil
}

// Open расшифровывает конверт. Функция пакета, а не метод Ring: метод был бы виден через
// псевдоним k1seal.Keyring любому импортёру libs/k1seal.
func Open(r *Ring, s Sealed, requestID string) (string, error) {
	if !r.usable() {
		return "", fmt.Errorf("%w: связка не задана", ErrKeyring)
	}
	kid, data, err := split(string(s))
	if err != nil {
		return "", err
	}
	aead, ok := r.st.aeads[kid]
	if !ok {
		return "", fmt.Errorf("%w: kid %q отсутствует в связке", ErrUnknownKey, kid)
	}
	if requestID == "" {
		return "", fmt.Errorf("%w: не задан идентификатор заявки", ErrOpen)
	}
	plain, err := aead.Open(nil, nil, data, aad(kid, requestID))
	if err != nil {
		// Ошибку GCM не оборачиваем: она ничего не добавляет, а причина одна — подлинность не подтверждена.
		return "", ErrOpen
	}
	return string(plain), nil
}

// String описывает связку без ключей.
func (r *Ring) String() string {
	if !r.usable() {
		return "k1seal.Keyring(пусто)"
	}
	return fmt.Sprintf("k1seal.Keyring(текущий=%s, ключей=%d)", r.st.current, len(r.st.kids))
}

// GoString для %#v.
func (r *Ring) GoString() string { return r.String() }

// Format перехватывает все глаголы fmt.
func (r *Ring) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, r.String()) }

// LogValue описывает связку в slog без ключей.
func (r *Ring) LogValue() slog.Value { return slog.StringValue(r.String()) }
