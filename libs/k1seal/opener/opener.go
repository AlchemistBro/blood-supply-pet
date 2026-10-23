// Package opener расшифровывает конверт номера случая пациента (ADR-0020).
//
// Импортировать пакет могут только services/matching (показ номера врачу с аудитом READ_PATIENT_CASE, NFR016)
// и services/inventory (передача закрепления в АИСТ-М, ИНТ-05). Правило проверяет tools/archtest.
package opener

import (
	"fmt"

	"github.com/AlchemistBro/blood-supply-pet/libs/k1seal"
	"github.com/AlchemistBro/blood-supply-pet/libs/k1seal/internal/envelope"
)

// Opener расшифровывает конверты ключами связки.
type Opener struct{ ring *k1seal.Keyring }

// New создаёт Opener над связкой ключей.
func New(kr *k1seal.Keyring) (*Opener, error) {
	if kr == nil || kr.CurrentKeyID() == "" {
		return nil, fmt.Errorf("%w: связка не задана", k1seal.ErrKeyring)
	}
	return &Opener{ring: kr}, nil
}

// Open возвращает номер случая. requestID — идентификатор заявки, к которой привязан конверт;
// с чужим идентификатором, подменённым байтом или неизвестным kid возвращается ошибка без открытого текста.
func (o *Opener) Open(s k1seal.Sealed, requestID string) (string, error) {
	plain, err := envelope.Open(o.ring, s, requestID)
	if err != nil {
		return "", fmt.Errorf("открытие номера случая: %w", err)
	}
	return plain, nil
}

// String не раскрывает ключей.
func (o *Opener) String() string { return "k1seal.Opener(" + o.ring.String() + ")" }
