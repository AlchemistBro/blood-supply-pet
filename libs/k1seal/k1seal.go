// Package k1seal шифрует номер случая пациента (класс данных K1) на уровне приложения (ADR-0020).
//
// Номер случая — сведения о здоровье, специальная категория ПДн (152-ФЗ, ст. 10) и врачебная тайна
// (323-ФЗ, ст. 13). Открытый номер нужен только matching (заявка врача) и inventory (ИНТ-05, АИСТ-М);
// transfer, Kafka, outbox, аудит и журналы видят только конверт Sealed.
//
// Пакет даёт тип Sealed, связку ключей Keyring и шифрование Keyring.Seal; его можно импортировать
// откуда угодно. Расшифровка — в libs/k1seal/opener, импорт которого tools/archtest разрешает
// только services/matching и services/inventory.
package k1seal

import (
	"fmt"
	"os"

	"github.com/AlchemistBro/blood-supply-pet/libs/k1seal/internal/envelope"
)

// EnvKeys — переменная окружения со связкой ключей: "kid:base64key,kid:base64key", первый — текущий.
// Её получают только matching и inventory.
const EnvKeys = "BS_K1_KEYS"

// MaxPlainLen — предел длины номера случая в байтах UTF-8 после обрезки пробелов.
const MaxPlainLen = envelope.MaxPlainLen

// Sealed — конверт v1.<kid>.<base64url(nonce|ciphertext|tag)>. fmt и slog не выводят его содержимое,
// MarshalJSON отдаёт конверт как есть.
type Sealed = envelope.Sealed

// Keyring — связка ключей AES-256: первый шифрует, все расшифровывают (ротация).
// Seal(plain, requestID) нормализует номер и шифрует его с AAD = заголовок конверта + request_id.
type Keyring = envelope.Ring

// Ошибки пакета; проверяются через errors.Is. Тексты не содержат номеров, конвертов и ключей.
var (
	ErrKeyring    = envelope.ErrKeyring
	ErrInput      = envelope.ErrInput
	ErrMalformed  = envelope.ErrMalformed
	ErrUnknownKey = envelope.ErrUnknownKey
	ErrOpen       = envelope.ErrOpen
)

// ParseKeyring разбирает связку ключей из строки формата BS_K1_KEYS.
func ParseKeyring(spec string) (*Keyring, error) {
	kr, err := envelope.ParseRing(spec)
	if err != nil {
		return nil, fmt.Errorf("связка %s: %w", EnvKeys, err)
	}
	return kr, nil
}

// KeyringFromEnv читает связку из переменной BS_K1_KEYS.
func KeyringFromEnv() (*Keyring, error) { return ParseKeyring(os.Getenv(EnvKeys)) }

// ParseSealed проверяет формат конверта (без ключа), например при чтении из базы или сообщения.
func ParseSealed(s string) (Sealed, error) {
	v, err := envelope.Parse(s)
	if err != nil {
		return "", fmt.Errorf("конверт номера случая: %w", err)
	}
	return v, nil
}
