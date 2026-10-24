package session

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// chunkSize — длина значения одной части: с именем и атрибутами cookie укладывается в 4 КБ браузера.
	chunkSize = 3800
	// maxChunks — предел частей: восемь частей по 3,8 КБ — больше, чем nginx примет в одном заголовке Cookie
	// по умолчанию (8 КБ), так что упереться в предел значит ошибиться в содержимом сессии.
	maxChunks = 8
)

// Store читает и пишет значение в набор cookie name.0, name.1, … (ADR-0018).
type Store struct {
	codec  *Codec
	name   string
	secure bool
}

// NewStore создаёт хранилище. secure — атрибут Secure (выключать только для отладки по http без localhost).
func NewStore(codec *Codec, name string, secure bool) *Store {
	return &Store{codec: codec, name: name, secure: secure}
}

// Name — базовое имя cookie.
func (s *Store) Name() string { return s.name }

func (s *Store) chunkName(i int) string { return s.name + "." + strconv.Itoa(i) }

// Load собирает части из запроса и расшифровывает в v. Возвращает срок жизни сессии.
func (s *Store) Load(r *http.Request, v any) (time.Time, error) {
	var b strings.Builder
	for i := 0; i < maxChunks; i++ {
		c, err := r.Cookie(s.chunkName(i))
		if err != nil {
			break
		}
		b.WriteString(c.Value)
	}
	if b.Len() == 0 {
		return time.Time{}, ErrNoSession
	}
	return s.codec.Open(s.name, b.String(), v)
}

// Save шифрует v до expires, пишет части и удаляет лишние части прежней версии из запроса r.
func (s *Store) Save(w http.ResponseWriter, r *http.Request, v any, expires time.Time) error {
	val, err := s.codec.Seal(s.name, v, expires)
	if err != nil {
		return err
	}
	n := (len(val) + chunkSize - 1) / chunkSize
	if n > maxChunks {
		return fmt.Errorf("сессия не помещается в %d cookie (%d байт)", maxChunks, len(val))
	}
	maxAge := int(expires.Sub(s.codec.clock.Now()).Seconds())
	for i := 0; i < n; i++ {
		end := min((i+1)*chunkSize, len(val))
		http.SetCookie(w, s.cookie(s.chunkName(i), val[i*chunkSize:end], maxAge))
	}
	for i := n; i < maxChunks; i++ {
		if _, err := r.Cookie(s.chunkName(i)); err == nil {
			http.SetCookie(w, s.cookie(s.chunkName(i), "", -1))
		}
	}
	return nil
}

// Clear удаляет все части, пришедшие в запросе.
func (s *Store) Clear(w http.ResponseWriter, r *http.Request) {
	for i := 0; i < maxChunks; i++ {
		if _, err := r.Cookie(s.chunkName(i)); err == nil {
			http.SetCookie(w, s.cookie(s.chunkName(i), "", -1))
		}
	}
}

func (s *Store) cookie(name, value string, maxAge int) *http.Cookie {
	// Secure включён по умолчанию (BS_COOKIE_SECURE); выключается только явно для отладки (ADR-0018).
	//nolint:gosec // G124: HttpOnly и SameSite=Lax заданы всегда, Secure — из конфигурации
	return &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	}
}
