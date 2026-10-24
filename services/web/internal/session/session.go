// Package session — сессия web в зашифрованной cookie (ADR-0018): AES-256-GCM стандартной библиотеки,
// срок жизни внутри шифртекста, имя cookie в дополнительных данных AEAD, ротация ключа, разбиение на части.
// Базы у web нет: всё состояние пользователя — в этой cookie, источник истины о нём — Keycloak.
package session

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

// KeySize — длина ключа AES-256.
const KeySize = 32

// Ошибки чтения cookie. Любая из них означает «нужен вход».
var (
	ErrNoSession = errors.New("сессии нет")
	ErrInvalid   = errors.New("cookie подделана, повреждена или зашифрована другим ключом")
	ErrExpired   = errors.New("срок сессии истёк")
)

// ParseKey разбирает ключ из переменной окружения: base64 (стандартный или URL, с выравниванием или без), 32 байта.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("ключ сессии не задан")
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if k, err := enc.DecodeString(s); err == nil {
			if len(k) != KeySize {
				return nil, fmt.Errorf("ключ сессии: нужно %d байт, получено %d", KeySize, len(k))
			}
			return k, nil
		}
	}
	return nil, errors.New("ключ сессии: ожидается base64")
}

// Codec шифрует и расшифровывает значения cookie.
type Codec struct {
	aeads []cipher.AEAD // первый шифрует, все расшифровывают (ротация ключа)
	clock clock.Clock
}

// NewCodec создаёт кодек. keys[0] — текущий ключ, остальные — прежние, только для чтения старых cookie.
func NewCodec(clk clock.Clock, keys ...[]byte) (*Codec, error) {
	if len(keys) == 0 {
		return nil, errors.New("ключ сессии не задан")
	}
	c := &Codec{clock: clk}
	for i, k := range keys {
		if len(k) != KeySize {
			return nil, fmt.Errorf("ключ сессии %d: нужно %d байт, получено %d", i, KeySize, len(k))
		}
		block, err := aes.NewCipher(k)
		if err != nil {
			return nil, fmt.Errorf("ключ сессии %d: %w", i, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("ключ сессии %d: %w", i, err)
		}
		c.aeads = append(c.aeads, aead)
	}
	return c, nil
}

// envelope — открытый текст cookie: срок жизни проверяется после расшифровки, подделать его нельзя.
type envelope struct {
	IssuedAt  int64           `json:"iat"`
	ExpiresAt int64           `json:"exp"`
	Value     json.RawMessage `json:"v"`
}

// Seal шифрует v для cookie name со сроком до expires. Результат — base64url без выравнивания.
func (c *Codec) Seal(name string, v any, expires time.Time) (string, error) {
	now := c.clock.Now()
	if !expires.After(now) {
		return "", errors.New("срок cookie уже истёк")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("сериализация сессии: %w", err)
	}
	plain, err := json.Marshal(envelope{IssuedAt: now.Unix(), ExpiresAt: expires.Unix(), Value: raw})
	if err != nil {
		return "", fmt.Errorf("сериализация сессии: %w", err)
	}
	aead := c.aeads[0]
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plain)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("nonce сессии: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, plain, []byte(name))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open расшифровывает значение cookie name в v и возвращает срок её жизни.
func (c *Codec) Open(name, value string, v any) (time.Time, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return time.Time{}, ErrInvalid
	}
	var plain []byte
	opened := false
	for _, aead := range c.aeads {
		ns := aead.NonceSize()
		if len(sealed) < ns+aead.Overhead() {
			return time.Time{}, ErrInvalid
		}
		if p, err := aead.Open(nil, sealed[:ns], sealed[ns:], []byte(name)); err == nil {
			plain, opened = p, true
			break
		}
	}
	if !opened {
		return time.Time{}, ErrInvalid
	}
	var env envelope
	if err := json.Unmarshal(plain, &env); err != nil {
		return time.Time{}, ErrInvalid
	}
	expires := time.Unix(env.ExpiresAt, 0).UTC()
	if !c.clock.Now().Before(expires) {
		return time.Time{}, ErrExpired
	}
	if err := json.Unmarshal(env.Value, v); err != nil {
		return time.Time{}, ErrInvalid
	}
	return expires, nil
}

// Data — сессия пользователя. ФИО, email и id_token не хранятся (ADR-0018): только то, что нужно для
// матрицы прав, вызовов сервисов и шапки (логин, роль, клиника).
type Data struct {
	Subject  string   `json:"sub"`
	Username string   `json:"usr"`
	Roles    []string `json:"roles"`
	ClinicID *int64   `json:"cid,omitempty"`
	// AccessToken передаётся в сервисы (Authorization: Bearer), RefreshToken обновляет его.
	AccessToken  string    `json:"at"`
	RefreshToken string    `json:"rt,omitempty"`
	AccessExpiry time.Time `json:"atx"`
	// CSRF — токен форм этой сессии; новый при каждом входе.
	CSRF string `json:"csrf"`
}

// Principal — участник для матрицы прав libs/httpx/auth.
func (d Data) Principal() auth.Principal {
	p := auth.Principal{Subject: d.Subject, Username: d.Username, ClinicID: d.ClinicID}
	for _, r := range d.Roles {
		p.Roles = append(p.Roles, auth.Role(r))
	}
	return p
}

// LoginState — состояние входа между переходом в Keycloak и callback (cookie bs_auth, 10 минут).
type LoginState struct {
	State    string `json:"st"`
	Nonce    string `json:"n"`
	Verifier string `json:"pkce"`
	// Next — локальный путь, куда вернуть пользователя после входа.
	Next string `json:"next,omitempty"`
}

// Equal сравнивает секреты (state, nonce, CSRF) за постоянное время; пустые значения не равны ничему.
func Equal(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// RandomToken — случайная строка для state, nonce и CSRF: 32 байта в base64url.
func RandomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("случайный токен: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
