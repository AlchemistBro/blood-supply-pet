package server

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

// maxNextLen — предел длины адреса возврата.
const maxNextLen = 512

// cleanLocalPath проверяет адрес возврата после входа и возвращает его в нормализованном виде.
// Защита от открытого перенаправления: браузеры читают `/\` и `\` как `//`, а http.Redirect чистит путь
// (path.Clean), поэтому `/x/../\evil` превратился бы в `/\evil`. Отклоняется:
//   - всё, что не начинается с одного `/`;
//   - обратная косая черта, пробелы, управляющие и не-ASCII символы (юникодные «слэши») — и в исходной
//     строке, и в раскодированном пути (`%5C`, `%09`, `%E2%88%95`);
//   - схема, хост, учётные данные, фрагмент;
//   - раскодированный или очищенный путь, начинающийся с `//` (`/%2F%2Fevil`).
func cleanLocalPath(next string) (string, bool) {
	if next == "" || len(next) > maxNextLen || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "", false
	}
	for i := 0; i < len(next); i++ {
		if c := next[i]; c <= 0x20 || c >= 0x7f || c == '\\' {
			return "", false
		}
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return "", false
	}
	if strings.HasPrefix(u.Path, "//") {
		return "", false
	}
	for _, r := range u.Path {
		if r <= 0x20 || r >= 0x7f || r == '\\' {
			return "", false
		}
	}
	cleaned := path.Clean(u.Path)
	if !strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, "//") {
		return "", false
	}
	out := (&url.URL{Path: cleaned, RawQuery: u.RawQuery}).String()
	if !strings.HasPrefix(out, "/") || strings.HasPrefix(out, "//") {
		return "", false
	}
	return out, true
}

// safeNext — адрес возврата после входа: только локальный путь, который ведёт на зарегистрированный
// маршрут web (белый список — сами маршруты mux). Всё остальное — на главную.
func (s *Server) safeNext(next string) string {
	cleaned, ok := cleanLocalPath(next)
	if !ok {
		return "/"
	}
	if s.mux != nil {
		u, err := url.Parse(cleaned)
		if err != nil {
			return "/"
		}
		if _, pattern := s.mux.Handler(&http.Request{Method: http.MethodGet, URL: u}); pattern == "" {
			return "/"
		}
	}
	return cleaned
}
