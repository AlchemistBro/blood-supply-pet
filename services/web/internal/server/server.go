// Package server — HTTP-каркас web: вход и выход через Keycloak, проверка сессии с обновлением токена,
// CSRF на всех небезопасных методах, заголовки безопасности на всех ответах (ADR-0018), страницы входа и 403.
// Экраны предметной области регистрируются поверх (этап 2, пакеты UI) и получают участника из контекста.
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/auditclient"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/authz"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/oidc"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/session"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/svcclient"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/ui"
)

// Имена cookie.
const (
	SessionCookie = "bs_session"
	LoginCookie   = "bs_auth"
	// loginStateTTL — сколько живёт состояние входа между переходом в Keycloak и callback.
	loginStateTTL = 10 * time.Minute
	// refreshBefore — за сколько до истечения access token обновлять его.
	refreshBefore = 30 * time.Second
)

// OIDC — порт входа (реализация — oidc.Client).
type OIDC interface {
	AuthURL(st session.LoginState, loginHint string) string
	Exchange(ctx context.Context, code string, st session.LoginState) (oidc.Result, error)
	Refresh(ctx context.Context, refreshToken string) (oidc.Result, error)
	LogoutURL() string
	ProviderOrigin() string
}

// Config — настройки web.
type Config struct {
	// SessionTTL — абсолютный срок сессии (BS_SESSION_TTL).
	SessionTTL time.Duration
	// CookieSecure — атрибут Secure у cookie (BS_COOKIE_SECURE).
	CookieSecure bool
	// HSTSMaxAge — max-age HSTS в секундах за TLS; 0 — не отправлять (BS_HSTS_MAX_AGE).
	HSTSMaxAge int
	// DemoAccess — блок «Демо-доступ» на странице входа (BS_DEMO_ACCESS).
	DemoAccess bool
	// Version — версия сборки для подвала.
	Version string
}

// Deps — зависимости сервера.
type Deps struct {
	Transfer  *svcclient.Client
	Inventory *svcclient.Client
	OIDC      OIDC
	Codec     *session.Codec
	Audit     auditclient.Recorder
	Clock     clock.Clock
	Log       *slog.Logger
}

// Server — обработчики и middleware web.
type Server struct {
	transfer  *svcclient.Client
	inventory *svcclient.Client
	ui        *ui.Renderer
	cfg       Config
	oidc      OIDC
	sessions  *session.Store
	login     *session.Store
	audit     auditclient.Recorder
	guard     *authz.Guard
	clock     clock.Clock
	log       *slog.Logger
	pages     *pages
	csp       string
	// mux — маршруты web: белый список адресов возврата после входа (safeNext).
	mux *http.ServeMux
}

// New собирает сервер.
func New(cfg Config, d Deps) (*Server, error) {
	if d.OIDC == nil || d.Codec == nil || d.Audit == nil || d.Clock == nil || d.Log == nil {
		return nil, errors.New("server: не хватает зависимостей")
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 8 * time.Hour
	}
	p, err := loadPages()
	if err != nil {
		return nil, err
	}
	screens, err := fs.Sub(templateFS, "templates/screens")
	if err != nil {
		return nil, fmt.Errorf("шаблоны экранов: %w", err)
	}
	renderer, err := ui.New(ui.Options{Pages: screens})
	if err != nil {
		return nil, err
	}
	s := &Server{
		transfer: d.Transfer, inventory: d.Inventory, ui: renderer,
		cfg: cfg, oidc: d.OIDC, audit: d.Audit, clock: d.Clock, log: d.Log, pages: p,
		sessions: session.NewStore(d.Codec, SessionCookie, cfg.CookieSecure),
		login:    session.NewStore(d.Codec, LoginCookie, cfg.CookieSecure),
		csp:      contentSecurityPolicy(d.OIDC.ProviderOrigin()),
	}
	s.guard = authz.New(d.Audit, d.Log, http.HandlerFunc(s.forbidden))
	return s, nil
}

// Guard — проверка прав для маршрутов экранов.
func (s *Server) Guard() *authz.Guard { return s.guard }

// Register регистрирует маршруты входа, выхода и главной страницы.
func (s *Server) Register(mux *http.ServeMux) {
	s.mux = mux
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("GET /auth/login", s.startLogin)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /{$}", s.home)
	mux.Handle("GET /static/", http.StripPrefix("/static/", s.ui.StaticHandler()))
	if s.transfer != nil && s.inventory != nil {
		s.registerOperator(mux)
	}
	if s.inventory != nil {
		s.registerStock(mux)
	}
}

// Middleware — цепочка web вместо bearer JWT (runtime.Runtime.Use): заголовки безопасности,
// проверка сессии, CSRF.
func (s *Server) Middleware() []httpx.Middleware {
	return []httpx.Middleware{s.securityHeaders, s.requireSession, s.checkCSRF}
}

// record пишет запись аудита; недоступность аудита не мешает входу и выходу (ADR-0017: отказ закрыт
// только для номера случая), ошибка уходит в лог.
func (s *Server) record(r *http.Request, e auditclient.Entry) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
	defer cancel()
	if err := s.audit.Record(ctx, e); err != nil {
		s.log.ErrorContext(r.Context(), "не удалось записать в аудит", "action", e.Action, "err", err)
	}
}

func (s *Server) saveSession(w http.ResponseWriter, r *http.Request, d session.Data, expires time.Time) error {
	if err := s.sessions.Save(w, r, d, expires); err != nil {
		return fmt.Errorf("сохранение сессии: %w", err)
	}
	return nil
}
