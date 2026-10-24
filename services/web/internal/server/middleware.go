package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/oidc"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/session"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/svcclient"
)

// contentSecurityPolicy — CSP без inline (ADR-0018). Источник Keycloak в form-action нужен форме выхода:
// POST /logout перенаправляет на end-session, а браузер проверяет form-action и для перенаправлений.
func contentSecurityPolicy(providerOrigin string) string {
	formAction := "form-action 'self'"
	if providerOrigin != "" {
		formAction += " " + providerOrigin
	}
	return strings.Join([]string{
		"default-src 'self'", "script-src 'self'", "style-src 'self'", "img-src 'self' data:",
		"connect-src 'self'", "font-src 'self'", "object-src 'none'", "base-uri 'self'",
		"frame-ancestors 'none'", formAction,
	}, "; ")
}

// securityHeaders ставит заголовки безопасности на каждый ответ, включая служебные маршруты и ошибки.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", s.csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		// HSTS имеет смысл только за TLS; по http браузер его игнорирует, а локальный стенд на :8080 не ломается.
		if s.cfg.HSTSMaxAge > 0 && (r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https") {
			h.Set("Strict-Transport-Security", "max-age="+strconv.Itoa(s.cfg.HSTSMaxAge))
		}
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// publicPath — маршруты без сессии: вход, служебные, статика.
func publicPath(p string) bool {
	switch p {
	case "/login", "/auth/login", "/auth/callback", "/healthz", "/readyz", "/metrics", "/openapi.yaml":
		return true
	}
	return strings.HasPrefix(p, "/static/")
}

type sessionKey struct{}

// currentSession — сессия запроса (после requireSession).
func currentSession(ctx context.Context) (session.Data, bool) {
	d, ok := ctx.Value(sessionKey{}).(session.Data)
	return d, ok
}

// requireSession читает сессию, обновляет access token перед истечением и кладёт в контекст участника
// (роли и клиника из токена) и токен для вызовов сервисов. Нет сессии, подделка, срок вышел, обновление
// не удалось — вход заново.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		var d session.Data
		expires, err := s.sessions.Load(r, &d)
		if err != nil {
			if !errors.Is(err, session.ErrNoSession) {
				s.log.InfoContext(r.Context(), "cookie сессии отклонена", "reason", err.Error())
				s.sessions.Clear(w, r)
			}
			svcclient.RedirectToLogin(w, r)
			return
		}
		if !d.AccessExpiry.After(s.clock.Now().Add(refreshBefore)) {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			res, err := s.oidc.Refresh(ctx, d.RefreshToken)
			cancel()
			if err == nil && (res.Principal.Subject == "" || res.Principal.Subject != d.Subject) {
				err = errors.New("участник сессии изменился при обновлении токена")
			}
			if err != nil {
				s.log.InfoContext(r.Context(), "токен не обновлён, нужен вход", "err", err)
				s.sessions.Clear(w, r)
				svcclient.RedirectToLogin(w, r)
				return
			}
			d = applyResult(d, res)
			// Перевыпуск cookie (ротация); абсолютный срок сессии не продлевается.
			if err := s.saveSession(w, r, d, expires); err != nil {
				s.log.ErrorContext(r.Context(), "сессия не сохранена", "err", err)
				s.sessions.Clear(w, r)
				svcclient.RedirectToLogin(w, r)
				return
			}
		}
		ctx := auth.WithPrincipal(r.Context(), d.Principal())
		ctx = svcclient.WithToken(ctx, d.AccessToken)
		ctx = context.WithValue(ctx, sessionKey{}, d)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// applyResult переносит участника и токены в сессию; CSRF-токен сохраняется.
func applyResult(d session.Data, res oidc.Result) session.Data {
	d.Subject, d.Username, d.ClinicID = res.Principal.Subject, res.Principal.Username, res.Principal.ClinicID
	d.Roles = nil
	for _, role := range res.Principal.Roles {
		d.Roles = append(d.Roles, string(role))
	}
	d.AccessToken, d.AccessExpiry = res.AccessToken, res.Expiry
	if res.RefreshToken != "" {
		d.RefreshToken = res.RefreshToken
	}
	return d
}

// checkCSRF требует токен сессии на каждом небезопасном методе: поле csrf_token формы или заголовок
// X-CSRF-Token (HTMX). Нет сессии или токен не совпал — 403.
func (s *Server) checkCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		var formErr error
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			r.Body = http.MaxBytesReader(w, r.Body, 6<<20)
			formErr = r.ParseMultipartForm(1 << 20) //nolint:gosec // всё тело выше ограничено MaxBytesReader до 6 МиБ; файлы свыше 1 МиБ временные и удаляются
			if r.MultipartForm != nil {
				defer func() { _ = r.MultipartForm.RemoveAll() }()
			}
		} else {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			formErr = r.ParseForm()
		}
		if formErr != nil {
			s.renderError(w, r, http.StatusBadRequest, "Неверная форма", "Обновите страницу и заполните форму ещё раз.")
			return
		}
		d, ok := currentSession(r.Context())
		token := r.Header.Get("X-CSRF-Token")
		if token == "" {
			token = r.PostFormValue("csrf_token")
		}
		if !ok || !session.Equal(token, d.CSRF) {
			s.renderError(w, r, http.StatusForbidden, "Форма устарела",
				"Действие не выполнено: форма устарела или открыта в другом окне. Обновите страницу и повторите.")
			return
		}
		next.ServeHTTP(w, r)
	})
}
