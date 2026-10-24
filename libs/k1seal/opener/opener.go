// Package opener — всё, что требует ключа номера случая пациента (ADR-0020): связка ключей из BS_K1_KEYS,
// шифрование и расшифровка конверта k1seal.Sealed. AES-256-GCM стандартной библиотеки, nonce 96 бит из crypto/rand,
// AAD = заголовок конверта "v1.<kid>." + request_id.
//
// Пакет прямо или транзитивно импортируют только services/matching (заявка врача, показ номера с аудитом
// READ_PATIENT_CASE, NFR016) и services/inventory (передача закрепления в АИСТ-М, ИНТ-05). Правило проверяет
// tools/archtest. Остальным достаточно типа k1seal.Sealed, в котором нет ни ключей, ни криптографии.
package opener

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/AlchemistBro/blood-supply-pet/libs/k1seal"
)

const keySize = 32

// Keyring — связка ключей AES-256: первый шифрует, все расшифровывают (ротация). Состояние за указателем,
// чтобы печать значения Keyring через рефлексию fmt не выводила внутренности AES.
type Keyring struct{ st *ringState }

type ringState struct {
	current string
	kids    []string
	aeads   map[string]cipher.AEAD
}

// KeyringFromEnv читает связку из переменной BS_K1_KEYS.
func KeyringFromEnv() (*Keyring, error) { return ParseKeyring(os.Getenv(k1seal.EnvKeys)) }

// ParseKeyring разбирает строку вида "kid:base64key,kid:base64key". Ключ — 32 байта в стандартном base64.
// Ошибки указывают номер записи или kid и не содержат ключей.
func ParseKeyring(spec string) (*Keyring, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, fmt.Errorf("связка %s: %w: список ключей пуст", k1seal.EnvKeys, k1seal.ErrKeyring)
	}
	st := &ringState{aeads: map[string]cipher.AEAD{}}
	for i, entry := range strings.Split(spec, ",") {
		n := i + 1
		kid, enc, ok := strings.Cut(strings.TrimSpace(entry), ":")
		if !ok {
			return nil, keyringErr("запись %d не в формате kid:base64key", n)
		}
		kid, enc = strings.TrimSpace(kid), strings.TrimSpace(enc)
		if !k1seal.ValidKeyID(kid) {
			return nil, keyringErr("запись %d: kid должен состоять из [A-Za-z0-9_-], 1..%d символов", n, k1seal.MaxKeyIDLen)
		}
		if _, dup := st.aeads[kid]; dup {
			return nil, keyringErr("kid %q повторяется", kid)
		}
		key, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return nil, keyringErr("ключ %q не в стандартном base64", kid)
		}
		if len(key) != keySize {
			clear(key)
			return nil, keyringErr("ключ %q длиной %d байт, нужно %d (AES-256)", kid, len(key), keySize)
		}
		aead, err := newAEAD(key)
		clear(key)
		if err != nil {
			return nil, fmt.Errorf("связка %s: %w: ключ %q: %w", k1seal.EnvKeys, k1seal.ErrKeyring, kid, err)
		}
		st.aeads[kid] = aead
		st.kids = append(st.kids, kid)
	}
	st.current = st.kids[0]
	return &Keyring{st: st}, nil
}

func keyringErr(format string, args ...any) error {
	return fmt.Errorf("связка %s: %w: %s", k1seal.EnvKeys, k1seal.ErrKeyring, fmt.Sprintf(format, args...))
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

func aad(kid, requestID string) []byte { return []byte(k1seal.Header(kid) + requestID) }

func (k *Keyring) usable() bool { return k != nil && k.st != nil && len(k.st.aeads) > 0 }

// CurrentKeyID — kid ключа, которым шифруются новые конверты.
func (k *Keyring) CurrentKeyID() string {
	if !k.usable() {
		return ""
	}
	return k.st.current
}

// KeyIDs — все kid связки в порядке задания, первый — текущий.
func (k *Keyring) KeyIDs() []string {
	if !k.usable() {
		return nil
	}
	return append([]string(nil), k.st.kids...)
}

// Seal нормализует номер случая (обрезка пробелов) и шифрует его текущим ключом с привязкой к заявке.
func (k *Keyring) Seal(plain, requestID string) (k1seal.Sealed, error) {
	if !k.usable() {
		return "", fmt.Errorf("%w: связка не задана", k1seal.ErrKeyring)
	}
	plain = strings.TrimSpace(plain)
	switch {
	case plain == "":
		return "", fmt.Errorf("%w: номер случая пуст", k1seal.ErrInput)
	case len(plain) > k1seal.MaxPlainLen:
		return "", fmt.Errorf("%w: номер случая длиннее %d байт", k1seal.ErrInput, k1seal.MaxPlainLen)
	case !utf8.ValidString(plain):
		return "", fmt.Errorf("%w: номер случая не в UTF-8", k1seal.ErrInput)
	case requestID == "":
		return "", fmt.Errorf("%w: не задан идентификатор заявки", k1seal.ErrInput)
	}
	kid := k.st.current
	// nonce = nil обязателен для NewGCMWithRandomNonce: случайный nonce создаёт сам AEAD.
	ct := k.st.aeads[kid].Seal(nil, nil, []byte(plain), aad(kid, requestID)) //nolint:gosec // G407 — ложное срабатывание, см. выше
	s, err := k1seal.Compose(kid, ct)
	if err != nil {
		return "", fmt.Errorf("сборка конверта: %w", err)
	}
	return s, nil
}

// Open возвращает номер случая. requestID — идентификатор заявки, к которой привязан конверт; с чужим
// идентификатором, подменённым байтом или неизвестным kid возвращается ошибка без открытого текста.
func (k *Keyring) Open(s k1seal.Sealed, requestID string) (string, error) {
	if !k.usable() {
		return "", fmt.Errorf("%w: связка не задана", k1seal.ErrKeyring)
	}
	kid, data, err := s.Parts()
	if err != nil {
		return "", fmt.Errorf("открытие номера случая: %w", err)
	}
	aead, ok := k.st.aeads[kid]
	if !ok {
		return "", fmt.Errorf("открытие номера случая: %w: kid %q отсутствует в связке", k1seal.ErrUnknownKey, kid)
	}
	if requestID == "" {
		return "", fmt.Errorf("открытие номера случая: %w: не задан идентификатор заявки", k1seal.ErrOpen)
	}
	plain, err := aead.Open(nil, nil, data, aad(kid, requestID))
	if err != nil {
		// Ошибку GCM не оборачиваем: причина одна — подлинность не подтверждена.
		return "", fmt.Errorf("открытие номера случая: %w", k1seal.ErrOpen)
	}
	return string(plain), nil
}

// String описывает связку без ключей.
func (k *Keyring) String() string {
	if !k.usable() {
		return "k1seal.Keyring(пусто)"
	}
	return fmt.Sprintf("k1seal.Keyring(текущий=%s, ключей=%d)", k.st.current, len(k.st.kids))
}

// GoString для %#v.
func (k *Keyring) GoString() string { return k.String() }

// Format перехватывает все глаголы fmt.
func (k *Keyring) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, k.String()) }

// LogValue описывает связку в slog без ключей.
func (k *Keyring) LogValue() slog.Value { return slog.StringValue(k.String()) }
