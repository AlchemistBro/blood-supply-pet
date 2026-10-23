package ui

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
)

// FlashCookieName — cookie с уведомлениями между POST и GET после перенаправления (без Secure, только отладка).
// FlashCookieNameSecure — то же при Secure: префикс __Host- требует Secure, Path=/ и запрещает Domain, поэтому
// поддомен не может подложить свою cookie. Отдельно от cookie сессии: та и так близка к пределу размера (ADR-0018),
// а уведомление живёт секунды.
//
// Принятый риск: значение не подписано. Подменить его может только тот, кто уже пишет cookie браузера пользователя
// (сам пользователь или скрипт на том же источнике, которого CSP не допускает); при этом он меняет лишь текст
// уведомления на собственном экране. Текст экранируется html/template, тон — из белого списка, длина — до 300 рун,
// уведомлений не больше трёх: внедрить разметку или действие нельзя, остаётся только ложная надпись. Подпись ключом
// сессии потребовала бы связать пакет ui с хранилищем сессии ради защиты от самого пользователя.
const (
	FlashCookieName       = "bs_flash"
	FlashCookieNameSecure = "__Host-bs_flash"
)

const (
	maxFlashes     = 3
	maxFlashRunes  = 300
	flashCookieTTL = 60 // секунд: хватает на перенаправление, не переживает следующий визит
)

var flashTones = map[Tone]bool{ToneSuccess: true, ToneInfo: true, ToneWarning: true, ToneDanger: true}

type flashWire struct {
	T Tone   `json:"t"`
	M string `json:"m"`
}

// SetFlash кладёт уведомления в cookie перед перенаправлением. Не больше maxFlashes, текст обрезается до
// maxFlashRunes. В тексте не должно быть patient_case_no и спецификации фенотипа: cookie видит браузер.
func SetFlash(w http.ResponseWriter, secure bool, flashes ...Flash) {
	if len(flashes) == 0 {
		return
	}
	if len(flashes) > maxFlashes {
		flashes = flashes[:maxFlashes]
	}
	http.SetCookie(w, flashCookie(encodeFlashes(flashes), flashCookieTTL, secure))
}

// TakeFlashes читает уведомления и удаляет cookie: каждое показывается один раз. Испорченное значение
// и неизвестный тон отбрасываются. Текст при выводе экранируется html/template, как любые данные.
func TakeFlashes(w http.ResponseWriter, r *http.Request, secure bool) []Flash {
	c, err := r.Cookie(flashCookieName(secure))
	if err != nil {
		return nil
	}
	http.SetCookie(w, flashCookie("", -1, secure))
	return decodeFlashes(c.Value)
}

func flashCookieName(secure bool) string {
	if secure {
		return FlashCookieNameSecure
	}
	return FlashCookieName
}

func flashCookie(value string, maxAge int, secure bool) *http.Cookie {
	// Secure включён по умолчанию (BS_COOKIE_SECURE в web); выключается только явно для отладки (ADR-0018).
	//nolint:gosec // G124: HttpOnly и SameSite=Lax заданы всегда, Secure — из конфигурации, с ним имя __Host-
	return &http.Cookie{
		Name: flashCookieName(secure), Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	}
}

func encodeFlashes(flashes []Flash) string {
	wire := make([]flashWire, 0, len(flashes))
	for _, f := range flashes {
		wire = append(wire, flashWire{T: f.Tone, M: truncateRunes(f.Text, maxFlashRunes)})
	}
	// Ошибка Marshal для среза структур со строками невозможна.
	b, _ := json.Marshal(wire)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeFlashes(value string) []Flash {
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil
	}
	var wire []flashWire
	if err := json.Unmarshal(b, &wire); err != nil {
		return nil
	}
	var out []Flash
	for _, f := range wire {
		if len(out) == maxFlashes {
			break
		}
		if !flashTones[f.T] || f.M == "" {
			continue
		}
		out = append(out, Flash{Tone: f.T, Text: truncateRunes(f.M, maxFlashRunes)})
	}
	return out
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
