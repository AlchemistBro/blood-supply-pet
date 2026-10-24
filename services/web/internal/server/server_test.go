package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/auditclient"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/oidc"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/oidc/oidctest"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/session"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/svcclient"
)

// ---------------------------------------------------------------- окружение теста

type fakeAudit struct {
	mu      sync.Mutex
	entries []auditclient.Entry
}

func (f *fakeAudit) Record(_ context.Context, e auditclient.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeAudit) find(action, result string) *auditclient.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.entries {
		if f.entries[i].Action == action && f.entries[i].Result == result {
			e := f.entries[i]
			return &e
		}
	}
	return nil
}

type env struct {
	t        *testing.T
	provider *oidctest.Provider
	clock    *clock.Mutable
	audit    *fakeAudit
	srv      *Server
	handler  http.Handler
	mux      *http.ServeMux
	service  *httptest.Server // сервис-приёмник токена пользователя
	gotToken string
	svcCode  int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, provider: oidctest.New(t), clock: clock.NewMutable(time.Now()), audit: &fakeAudit{}, svcCode: http.StatusOK}
	// Как настроенный realm-blood-supply.json: в токенах нет ФИО и email (проверка — TestPersonalData*).
	e.provider.SetPersonalClaims(false)
	e.service =httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.gotToken = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(e.svcCode)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(e.service.Close)

	client, err := oidc.New(context.Background(), oidc.Config{
		Issuer: e.provider.URL, ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret,
		RedirectURL: "http://localhost:8080/auth/callback", PostLogoutRedirectURL: "http://localhost:8080/login",
		Audience: oidctest.Audience,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, session.KeySize)
	_, _ = rand.Read(key)
	codec, err := session.NewCodec(e.clock, key)
	if err != nil {
		t.Fatal(err)
	}
	e.srv, err = New(Config{SessionTTL: 8 * time.Hour, CookieSecure: true, HSTSMaxAge: 31536000, DemoAccess: true, Version: "test"},
		Deps{OIDC: client, Codec: codec, Audit: e.audit, Clock: e.clock, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	e.srv.Register(mux)
	// Тестовые маршруты: операция по матрице и вызов сервиса от имени пользователя.
	mux.Handle("POST /tasks/{id}/confirm", e.srv.Guard().Require(auth.OpConfirmTransfer)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })))
	svc := svcclient.New("transfer", e.service.URL)
	mux.HandleFunc("GET /tasks", func(w http.ResponseWriter, r *http.Request) {
		if err := svc.GetJSON(r.Context(), "/obligations", &struct{}{}); err != nil {
			if svcclient.LoginOnUnauthorized(w, r, err) {
				return
			}
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	e.mux = mux
	e.handler = httpx.Chain(mux, e.srv.Middleware()...)
	return e
}

// jar — cookie браузера: Set-Cookie с Max-Age<0 удаляет.
type jar map[string]string

func (j jar) update(rec *httptest.ResponseRecorder) {
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(j, c.Name)
		} else {
			j[c.Name] = c.Value
		}
	}
}

func (j jar) hasPrefix(p string) bool {
	for name := range j {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func (e *env) do(method, target string, j jar, mutate func(*http.Request)) *httptest.ResponseRecorder {
	e.t.Helper()
	var body io.Reader
	r := httptest.NewRequest(method, target, body)
	for name, val := range j {
		r.AddCookie(&http.Cookie{Name: name, Value: val})
	}
	if mutate != nil {
		mutate(r)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, r)
	if j != nil {
		j.update(rec)
	}
	return rec
}

func postForm(values url.Values) func(*http.Request) {
	return func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(values.Encode()))
		r.ContentLength = int64(len(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
}

// login проходит вход целиком и возвращает cookie браузера.
func (e *env) login(u oidctest.User, next string) jar {
	e.t.Helper()
	e.provider.LoginAs(u)
	j := jar{}
	rec := e.do(http.MethodGet, "/auth/login?hint="+u.Username+"&next="+url.QueryEscape(next), j, nil)
	if rec.Code != http.StatusFound {
		e.t.Fatalf("/auth/login: %d", rec.Code)
	}
	cb := e.provider.Authorize(e.t, rec.Header().Get("Location"))
	rec = e.do(http.MethodGet, "/auth/callback?"+cb.RawQuery, j, nil)
	if rec.Code != http.StatusSeeOther {
		e.t.Fatalf("callback: %d %s", rec.Code, rec.Body.String())
	}
	return j
}

var (
	operator  = oidctest.User{Username: "operator-c001", Roles: []auth.Role{auth.RoleOperator}, ClinicID: clinicPtr(1)}
	physician = oidctest.User{Username: "physician1", Roles: []auth.Role{auth.RolePhysician}, ClinicID: clinicPtr(1)}
)

func clinicPtr(id int64) *int64 { return &id }

func csrfOf(t *testing.T, e *env, j jar) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for name, val := range j {
		r.AddCookie(&http.Cookie{Name: name, Value: val})
	}
	var d session.Data
	if _, err := e.srv.sessions.Load(r, &d); err != nil {
		t.Fatal(err)
	}
	return d.CSRF
}

// ---------------------------------------------------------------- вход

// Полный вход: PKCE и login_hint в Keycloak, callback со state и nonce, сессия в cookie, LOGIN в аудит.
func TestLoginFlow(t *testing.T) {
	e := newEnv(t)
	j := e.login(operator, "/tasks")
	if !j.hasPrefix("bs_session.") {
		t.Fatal("после входа должна появиться cookie сессии")
	}
	if j.hasPrefix("bs_auth.") {
		t.Fatal("cookie состояния входа удаляется после callback")
	}
	if e.provider.LastLoginHint() != "operator-c001" {
		t.Fatal("login_hint блока «Демо-доступ» не передан")
	}
	if a := e.audit.find(auditc.ActionLogin, auditc.ResultSuccess); a == nil || a.Principal == nil || a.Principal.Username != "operator-c001" {
		t.Fatalf("вход не записан в аудит: %+v", e.audit.entries)
	}
	rec := e.do(http.MethodGet, "/", j, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("главная после входа: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "operator-c001") || !strings.Contains(body, "Оператор клиники") || !strings.Contains(body, "№ 1") {
		t.Fatalf("в шапке логин, роль и клиника: %s", body)
	}
}

func TestLoginRedirectsToNextOnlyLocally(t *testing.T) {
	e := newEnv(t)
	for next, want := range map[string]string{
		"/tasks?status=PENDING": "/tasks?status=PENDING",
		"//evil.example/x":      "/",
		"https://evil.example/": "/",
		"/\\evil.example":       "/",
		"/x/../\\evil.example":  "/",
		"/%5C":                  "/",
		"/..%2F%5C":             "/",
		"/\t/evil.example":      "/",
		"/∕evil.example":        "/",
		"/unknown-page":         "/",
		"":                      "/",
	} {
		e.provider.LoginAs(operator)
		j := jar{}
		rec := e.do(http.MethodGet, "/auth/login?next="+url.QueryEscape(next), j, nil)
		cb := e.provider.Authorize(t, rec.Header().Get("Location"))
		rec = e.do(http.MethodGet, "/auth/callback?"+cb.RawQuery, j, nil)
		if rec.Header().Get("Location") != want {
			t.Fatalf("next=%q: переход на %q, ожидался %q", next, rec.Header().Get("Location"), want)
		}
	}
}

// Неверный state: вход не выполняется, сессии нет, попытка в аудите.
func TestCallbackRejectsWrongState(t *testing.T) {
	e := newEnv(t)
	j := jar{}
	rec := e.do(http.MethodGet, "/auth/login", j, nil)
	cb := e.provider.Authorize(t, rec.Header().Get("Location"))
	q := cb.Query()
	q.Set("state", "чужой-state")
	rec = e.do(http.MethodGet, "/auth/callback?"+q.Encode(), j, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("неверный state: ожидался 400, получен %d", rec.Code)
	}
	if j.hasPrefix("bs_session.") {
		t.Fatal("при неверном state сессия не создаётся")
	}
	if e.provider.TokenCalls() != 0 {
		t.Fatal("при неверном state код не обменивается")
	}
	if e.audit.find(auditc.ActionLogin, auditc.ResultDenied) == nil {
		t.Fatal("неудачный вход пишется в аудит")
	}
	if !strings.Contains(rec.Body.String(), "Войдите ещё раз") {
		t.Fatalf("сообщение человеческим языком: %s", rec.Body.String())
	}
}

// Неверный nonce в id_token: вход не выполняется.
func TestCallbackRejectsWrongNonce(t *testing.T) {
	e := newEnv(t)
	e.provider.ForceNonce("чужой-nonce")
	j := jar{}
	rec := e.do(http.MethodGet, "/auth/login", j, nil)
	cb := e.provider.Authorize(t, rec.Header().Get("Location"))
	rec = e.do(http.MethodGet, "/auth/callback?"+cb.RawQuery, j, nil)
	if rec.Code != http.StatusBadRequest || j.hasPrefix("bs_session.") {
		t.Fatalf("неверный nonce: %d, сессия %v", rec.Code, j.hasPrefix("bs_session."))
	}
}

// Callback без cookie состояния входа (открыт в другом браузере или через 10 минут) — отказ.
func TestCallbackWithoutLoginState(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodGet, "/auth/login", jar{}, nil)
	cb := e.provider.Authorize(t, rec.Header().Get("Location"))
	j := jar{}
	rec = e.do(http.MethodGet, "/auth/callback?"+cb.RawQuery, j, nil)
	if rec.Code != http.StatusBadRequest || j.hasPrefix("bs_session.") {
		t.Fatalf("без состояния входа: %d", rec.Code)
	}

	// Состояние входа живёт 10 минут.
	j = jar{}
	rec = e.do(http.MethodGet, "/auth/login", j, nil)
	cb = e.provider.Authorize(t, rec.Header().Get("Location"))
	e.clock.Advance(11 * time.Minute)
	rec = e.do(http.MethodGet, "/auth/callback?"+cb.RawQuery, j, nil)
	if rec.Code != http.StatusBadRequest || j.hasPrefix("bs_session.") {
		t.Fatalf("просроченное состояние входа: %d", rec.Code)
	}
}

func TestCallbackProviderError(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodGet, "/auth/callback?error=access_denied&state=x", jar{}, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Вход не выполнен") {
		t.Fatalf("ошибка провайдера: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLoginRejectsBadHint(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodGet, "/auth/login?hint="+url.QueryEscape("<script>"), jar{}, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("/auth/login: %d", rec.Code)
	}
	u, _ := url.Parse(rec.Header().Get("Location"))
	if u.Query().Get("login_hint") != "" {
		t.Fatal("недопустимый login_hint не передаётся в Keycloak")
	}
}

func TestLoginPageHasDemoAccess(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodGet, "/login", nil, nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Демо-доступ") {
		t.Fatalf("страница входа: %d", rec.Code)
	}
	for _, u := range []string{"physician1", "operator-c001", "analyst1", "duty1"} {
		if !strings.Contains(body, "/auth/login?hint="+u) {
			t.Fatalf("нет кнопки входа для %s", u)
		}
	}
	if strings.Contains(body, "<script") || strings.Contains(body, "style=") {
		t.Fatal("в шаблонах нет встроенных скриптов и стилей (CSP без inline)")
	}
}

// ---------------------------------------------------------------- сессия

func TestProtectedPageWithoutSessionRedirectsToLogin(t *testing.T) {
	e := newEnv(t)
	rec := e.do(http.MethodGet, "/tasks?status=PENDING", nil, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?next=%2Ftasks%3Fstatus%3DPENDING" {
		t.Fatalf("без сессии: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rec = e.do(http.MethodGet, "/tasks", nil, func(r *http.Request) { r.Header.Set("HX-Request", "true") })
	if rec.Header().Get("HX-Redirect") == "" {
		t.Fatal("для HTMX — HX-Redirect")
	}
}

// Подделанная cookie сессии отклоняется: вход заново, cookie удаляются.
func TestForgedSessionCookieRejected(t *testing.T) {
	e := newEnv(t)
	j := e.login(operator, "/")
	for name, val := range j {
		if strings.HasPrefix(name, "bs_session.") {
			b := []byte(val)
			if b[10] == 'A' {
				b[10] = 'B'
			} else {
				b[10] = 'A'
			}
			j[name] = string(b)
		}
	}
	rec := e.do(http.MethodGet, "/", j, nil)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("подделанная cookie: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if j.hasPrefix("bs_session.") {
		t.Fatal("подделанная cookie должна удаляться")
	}

	j = jar{"bs_session.0": "подделка"}
	if rec := e.do(http.MethodGet, "/", j, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("произвольная cookie: %d", rec.Code)
	}
}

// Просроченная сессия (BS_SESSION_TTL) — вход заново.
func TestExpiredSessionRejected(t *testing.T) {
	e := newEnv(t)
	j := e.login(operator, "/")
	e.clock.Advance(8*time.Hour + time.Second)
	rec := e.do(http.MethodGet, "/", j, nil)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("просроченная сессия: %d", rec.Code)
	}
}

// Истёкший access token обновляется по refresh token, cookie перевыпускается.
func TestAccessTokenRefresh(t *testing.T) {
	e := newEnv(t)
	j := e.login(operator, "/")
	before := csrfOf(t, e, j)
	old := j["bs_session.0"]
	calls := e.provider.TokenCalls()
	e.clock.Advance(10 * time.Minute) // access token живёт 5 минут
	rec := e.do(http.MethodGet, "/", j, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("после обновления токена страница открывается: %d", rec.Code)
	}
	if e.provider.TokenCalls() != calls+1 {
		t.Fatal("ожидался один запрос обновления токена")
	}
	if j["bs_session.0"] == old {
		t.Fatal("cookie сессии перевыпускается после обновления")
	}
	if csrfOf(t, e, j) != before {
		t.Fatal("CSRF-токен сессии при обновлении не меняется")
	}
}

// Обновление не удалось (сессия Keycloak завершена) — вход заново.
func TestRefreshFailureRedirectsToLogin(t *testing.T) {
	e := newEnv(t)
	j := e.login(operator, "/")
	e.provider.RevokeRefreshTokens()
	e.clock.Advance(10 * time.Minute)
	rec := e.do(http.MethodGet, "/", j, nil)
	if rec.Code != http.StatusSeeOther || j.hasPrefix("bs_session.") {
		t.Fatalf("неудачное обновление: %d, сессия %v", rec.Code, j.hasPrefix("bs_session."))
	}
}

// ---------------------------------------------------------------- права, CSRF, сервисы

// Чужая роль: 403 «Недостаточно прав» и запись в аудит.
func TestForeignRoleGets403AndAudit(t *testing.T) {
	e := newEnv(t)
	j := e.login(physician, "/")
	rec := e.do(http.MethodPost, "/tasks/1/confirm", j, postForm(url.Values{"csrf_token": {csrfOf(t, e, j)}}))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Недостаточно прав") {
		t.Fatalf("врачу подтверждение запрещено: %d %s", rec.Code, rec.Body.String())
	}
	a := e.audit.find(auditc.ActionAccessDenied, auditc.ResultDenied)
	if a == nil || a.ObjectID != string(auth.OpConfirmTransfer) || a.Principal.Username != "physician1" {
		t.Fatalf("отказ в аудите: %+v", e.audit.entries)
	}

	j = e.login(operator, "/")
	rec = e.do(http.MethodPost, "/tasks/1/confirm", j, postForm(url.Values{"csrf_token": {csrfOf(t, e, j)}}))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("оператору подтверждение разрешено: %d", rec.Code)
	}
}

// POST без CSRF-токена — 403, с чужим токеном — 403, с токеном в заголовке HTMX — проходит.
func TestCSRF(t *testing.T) {
	e := newEnv(t)
	j := e.login(operator, "/")
	if rec := e.do(http.MethodPost, "/tasks/1/confirm", j, postForm(url.Values{})); rec.Code != http.StatusForbidden {
		t.Fatalf("POST без CSRF: ожидался 403, получен %d", rec.Code)
	}
	if rec := e.do(http.MethodPost, "/tasks/1/confirm", j, postForm(url.Values{"csrf_token": {"чужой"}})); rec.Code != http.StatusForbidden {
		t.Fatalf("POST с чужим CSRF: ожидался 403, получен %d", rec.Code)
	}
	token := csrfOf(t, e, j)
	rec := e.do(http.MethodPost, "/tasks/1/confirm", j, func(r *http.Request) {
		r.Header.Set("HX-Request", "true")
		r.Header.Set("X-CSRF-Token", token)
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("HTMX с токеном в заголовке: %d", rec.Code)
	}
	// Выход тоже только с CSRF: чужая страница не разлогинит пользователя.
	if rec := e.do(http.MethodPost, "/logout", j, postForm(url.Values{})); rec.Code != http.StatusForbidden {
		t.Fatalf("выход без CSRF: ожидался 403, получен %d", rec.Code)
	}
	if !j.hasPrefix("bs_session.") {
		t.Fatal("без CSRF сессия не удаляется")
	}
}

// Выход: аудит LOGOUT, cookie удалены, переход на end-session Keycloak.
func TestLogout(t *testing.T) {
	e := newEnv(t)
	j := e.login(operator, "/")
	rec := e.do(http.MethodPost, "/logout", j, postForm(url.Values{"csrf_token": {csrfOf(t, e, j)}}))
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), e.provider.URL+"/logout?") {
		t.Fatalf("выход: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if j.hasPrefix("bs_session.") {
		t.Fatal("после выхода cookie сессии удалены")
	}
	if e.audit.find(auditc.ActionLogout, auditc.ResultSuccess) == nil {
		t.Fatal("выход пишется в аудит")
	}
	if rec := e.do(http.MethodGet, "/logout", j, nil); rec.Code == http.StatusSeeOther && strings.Contains(rec.Header().Get("Location"), "/logout?") {
		t.Fatal("выход по GET не выполняется")
	}
}

// JWT пользователя уходит в сервис; 401 от сервиса — переход на вход.
func TestServiceCallUsesUserTokenAnd401RedirectsToLogin(t *testing.T) {
	e := newEnv(t)
	j := e.login(operator, "/")
	if rec := e.do(http.MethodGet, "/tasks", j, nil); rec.Code != http.StatusOK {
		t.Fatalf("вызов сервиса: %d", rec.Code)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for name, val := range j {
		r.AddCookie(&http.Cookie{Name: name, Value: val})
	}
	var d session.Data
	if _, err := e.srv.sessions.Load(r, &d); err != nil {
		t.Fatal(err)
	}
	if e.gotToken == "" || e.gotToken != d.AccessToken {
		t.Fatal("в сервис передаётся access token пользователя из сессии")
	}

	e.svcCode = http.StatusUnauthorized
	rec := e.do(http.MethodGet, "/tasks", j, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?next=%2Ftasks" {
		t.Fatalf("401 от сервиса: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

// ---------------------------------------------------------------- заголовки безопасности

func checkSecurityHeaders(t *testing.T, name string, rec *httptest.ResponseRecorder, origin string) {
	t.Helper()
	h := rec.Header()
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "style-src 'self'", "frame-ancestors 'none'",
		"object-src 'none'", "base-uri 'self'", "form-action 'self' " + origin} {
		if !strings.Contains(csp, want) {
			t.Fatalf("%s: в CSP нет %q: %s", name, want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Fatalf("%s: CSP без inline: %s", name, csp)
	}
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" || h.Get("Referrer-Policy") != "same-origin" {
		t.Fatalf("%s: заголовки безопасности: %v", name, h)
	}
}

// Заголовки безопасности на всех ответах: публичные, защищённые, служебные, ошибки и переходы.
func TestSecurityHeadersOnAllResponses(t *testing.T) {
	e := newEnv(t)
	origin := e.provider.URL
	checkSecurityHeaders(t, "/login", e.do(http.MethodGet, "/login", nil, nil), origin)
	checkSecurityHeaders(t, "/healthz", e.do(http.MethodGet, "/healthz", nil, nil), origin)
	checkSecurityHeaders(t, "переход без сессии", e.do(http.MethodGet, "/", nil, nil), origin)
	checkSecurityHeaders(t, "/auth/login", e.do(http.MethodGet, "/auth/login", jar{}, nil), origin)
	checkSecurityHeaders(t, "ошибка callback", e.do(http.MethodGet, "/auth/callback?state=x&code=y", jar{}, nil), origin)
	j := e.login(physician, "/")
	checkSecurityHeaders(t, "главная", e.do(http.MethodGet, "/", j, nil), origin)
	checkSecurityHeaders(t, "403", e.do(http.MethodPost, "/tasks/1/confirm", j, postForm(url.Values{"csrf_token": {csrfOf(t, e, j)}})), origin)
	checkSecurityHeaders(t, "CSRF 403", e.do(http.MethodPost, "/tasks/1/confirm", j, postForm(url.Values{})), origin)
	checkSecurityHeaders(t, "404", e.do(http.MethodGet, "/no-such-page", j, nil), origin)

	if rec := e.do(http.MethodGet, "/", j, nil); rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("страницы с сессией не кэшируются")
	}
}

// HSTS — только за TLS (nginx передаёт X-Forwarded-Proto: https).
func TestHSTSOnlyOverTLS(t *testing.T) {
	e := newEnv(t)
	if rec := e.do(http.MethodGet, "/login", nil, nil); rec.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("по http HSTS не отправляется")
	}
	rec := e.do(http.MethodGet, "/login", nil, func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") })
	if rec.Header().Get("Strict-Transport-Security") != "max-age=31536000" {
		t.Fatalf("за TLS HSTS: %q", rec.Header().Get("Strict-Transport-Security"))
	}
}

// В шапке только логин: ФИО не выводится (scope profile даёт его Keycloak, но web его не хранит).
func TestHeaderShowsLoginOnly(t *testing.T) {
	e := newEnv(t)
	j := e.login(operator, "/")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for name, val := range j {
		r.AddCookie(&http.Cookie{Name: name, Value: val})
	}
	var raw map[string]any
	if _, err := e.srv.sessions.Load(r, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"name", "given_name", "family_name", "email", "id_token"} {
		if _, ok := raw[k]; ok {
			t.Fatalf("в сессии не хранится %s", k)
		}
	}
	assertNoPersonalData(t, raw)
}

// assertNoPersonalData — в расшифрованной сессии, включая тело access token, нет ФИО и email.
func assertNoPersonalData(t *testing.T, raw map[string]any) {
	t.Helper()
	plain, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	at, _ := raw["at"].(string)
	parts := strings.Split(at, ".")
	if len(parts) != 3 {
		t.Fatalf("в сессии нет access token: %q", at)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"name", "given_name", "family_name", "email"} {
		if _, ok := claims[k]; ok {
			t.Fatalf("в access token из cookie есть %s", k)
		}
	}
	for _, s := range []string{oidctest.FullName, oidctest.Email, "Иванова"} {
		if strings.Contains(string(plain), s) || strings.Contains(string(payload), s) {
			t.Fatalf("в cookie сессии есть %q", s)
		}
	}
}

// Ненастроенный Keycloak (ФИО и email в токене): сессия не создаётся, пользователь видит причину.
func TestPersonalDataInTokenRefusesLogin(t *testing.T) {
	e := newEnv(t)
	e.provider.SetPersonalClaims(true)
	j := jar{}
	rec := e.do(http.MethodGet, "/auth/login", j, nil)
	cb := e.provider.Authorize(t, rec.Header().Get("Location"))
	rec = e.do(http.MethodGet, "/auth/callback?"+cb.RawQuery, j, nil)
	if rec.Code != http.StatusBadRequest || j.hasPrefix("bs_session.") {
		t.Fatalf("токен с ФИО: %d, сессия %v", rec.Code, j.hasPrefix("bs_session."))
	}
	if !strings.Contains(rec.Body.String(), "ИТ-службу") || strings.Contains(rec.Body.String(), oidctest.FullName) {
		t.Fatalf("сообщение без самих данных: %s", rec.Body.String())
	}
}
