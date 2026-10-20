package auth

import (
	"context"
	"net/http"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
)

type ctxKey struct{}

// WithPrincipal кладёт участника в контекст.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext возвращает участника запроса.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// Denied вызывается при отказе: сервисы пишут ACCESS_DENIED в аудит.
type Denied func(r *http.Request, p *Principal, op Operation)

// Authenticate проверяет токен у всех запросов, кроме служебных путей, и кладёт участника в контекст.
func Authenticate(v Verifier) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/healthz", "/readyz", "/metrics", "/openapi.yaml":
				next.ServeHTTP(w, r)
				return
			}
			raw := BearerToken(r)
			if raw == "" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="blood-supply"`)
				httpx.WriteProblem(w, r, http.StatusUnauthorized, "Требуется вход", "Передайте токен в заголовке Authorization.")
				return
			}
			p, err := v.Verify(r.Context(), raw)
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer realm="blood-supply", error="invalid_token"`)
				httpx.WriteProblem(w, r, http.StatusUnauthorized, "Требуется вход", "Токен недействителен или просрочен.")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

// Require проверяет право на операцию по матрице. При отказе — 403 и вызов onDenied (аудит).
func Require(op Operation, onDenied Denied) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := FromContext(r.Context())
			if !ok {
				httpx.WriteProblem(w, r, http.StatusUnauthorized, "Требуется вход", "")
				return
			}
			if !Allowed(op, p.Roles) {
				if onDenied != nil {
					onDenied(r, &p, op)
				}
				httpx.WriteProblem(w, r, http.StatusForbidden, "Недостаточно прав",
					"Операция недоступна вашей роли. Обратитесь в ИТ-службу.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireHandler — то же, что Require, но для одного обработчика.
func RequireHandler(op Operation, onDenied Denied, h http.Handler) http.Handler {
	return Require(op, onDenied)(h)
}

// SameClinic проверяет, что участник работает от имени клиники clinicID (или не привязан к клинике:
// дежурный и ИТ-служба видят все). Возвращает false, если клиника участника другая.
func SameClinic(p Principal, clinicID int64) bool {
	if p.ClinicID == nil {
		return p.HasRole(RoleDuty) || p.HasRole(RoleITService) || p.HasRole(RoleService)
	}
	return *p.ClinicID == clinicID
}
