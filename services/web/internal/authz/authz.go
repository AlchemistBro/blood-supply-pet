// Package authz — права в web: операция по общей матрице libs/httpx/auth (роли из access token), сверка
// clinic_id из токена с клиникой объекта, страница 403 и запись отказа в аудит (security.md, ADR-0017).
// Участника кладёт в контекст проверка сессии (internal/server); здесь только решение «можно / нельзя».
package authz

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/auditclient"
)

// LoginPath — куда отправлять запрос без входа.
const LoginPath = "/login"

// ClinicOf извлекает клинику объекта из запроса (путь, параметр, загруженный объект).
type ClinicOf func(r *http.Request) (int64, error)

// Guard проверяет права и фиксирует отказы.
type Guard struct {
	audit     auditclient.Recorder
	log       *slog.Logger
	forbidden http.Handler
}

// New создаёт проверку. forbidden рисует страницу 403 «Недостаточно прав».
func New(audit auditclient.Recorder, log *slog.Logger, forbidden http.Handler) *Guard {
	return &Guard{audit: audit, log: log, forbidden: forbidden}
}

// Require пропускает запрос, если операция разрешена хотя бы одной роли участника.
func (g *Guard) Require(op auth.Operation) httpx.Middleware {
	return g.RequireClinic(op, nil)
}

// RequireClinic проверяет операцию и, если clinicOf задан, что клиника объекта — клиника участника
// (дежурный и ИТ-служба без clinic_id видят все клиники, auth.SameClinic).
func (g *Guard) RequireClinic(op auth.Operation, clinicOf ClinicOf) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := auth.FromContext(r.Context())
			if !ok {
				http.Redirect(w, r, LoginPath, http.StatusSeeOther)
				return
			}
			if !auth.Allowed(op, p.Roles) {
				g.Deny(w, r, p, op, "role")
				return
			}
			if clinicOf != nil {
				id, err := clinicOf(r)
				if err != nil || !auth.SameClinic(p, id) {
					g.Deny(w, r, p, op, "clinic")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Deny пишет ACCESS_DENIED в аудит и показывает страницу 403. Недоступность аудита не меняет ответа:
// отказ остаётся отказом, ошибка записи уходит в лог.
func (g *Guard) Deny(w http.ResponseWriter, r *http.Request, p auth.Principal, op auth.Operation, reason string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
	defer cancel()
	err := g.audit.Record(ctx, auditclient.Entry{
		Action: auditc.ActionAccessDenied, ObjectType: "operation", ObjectID: string(op), Result: auditc.ResultDenied,
		Principal: &p, Details: map[string]any{"reason": reason, "path": r.URL.Path, "method": r.Method},
	})
	if err != nil {
		g.log.ErrorContext(r.Context(), "не удалось записать отказ в аудит", "operation", string(op), "err", err)
	}
	g.forbidden.ServeHTTP(w, r)
}

// ClinicID — клиника участника из токена; false для ролей без клиники.
func ClinicID(ctx context.Context) (int64, bool) {
	p, ok := auth.FromContext(ctx)
	if !ok || p.ClinicID == nil {
		return 0, false
	}
	return *p.ClinicID, true
}
