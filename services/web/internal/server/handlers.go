package server

import (
	"errors"
	"net/http"
	"regexp"

	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/auditclient"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/oidc"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/session"
)

// demoUsers — учётные записи realm для блока «Демо-доступ» (deploy/compose/keycloak/realm-blood-supply.json).
// Пароли вводятся в Keycloak; web подставляет только логин через login_hint.
var demoUsers = []demoUser{
	{Login: "physician1", Label: "Войти как врач"},
	{Login: "operator-c001", Label: "Войти как оператор клиники № 1"},
	{Login: "analyst1", Label: "Войти как аналитик"},
	{Login: "duty1", Label: "Войти как дежурный"},
}

// loginHintPattern — допустимый login_hint: логины realm, ничего больше в Keycloak не передаётся.
var loginHintPattern = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)

const (
	msgRetry     = "Вход не выполнен: ссылка входа устарела или открыта в другом окне. Войдите ещё раз."
	msgCancelled = "Вход не выполнен: Keycloak отклонил или отменил вход. Войдите ещё раз."
	msgFailed    = "Вход не выполнен: не удалось подтвердить вход в Keycloak. Войдите ещё раз."
	// msgPersonalData — Keycloak настроен неверно: токен с ФИО или email не хранится (ADR-0018).
	msgPersonalData = "Вход невозможен: Keycloak передаёт в токене персональные данные (ФИО или email). Обратитесь в ИТ-службу."
)

// loginPage — страница входа с блоком «Демо-доступ».
func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	s.renderLogin(w, r, http.StatusOK, "", s.safeNext(r.URL.Query().Get("next")))
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, status int, msg, next string) {
	v := s.view(r, "Вход")
	v.Error, v.Next = msg, next
	if s.cfg.DemoAccess {
		v.Demo = demoUsers
	}
	s.render(w, r, status, "login", v)
}

// startLogin создаёт состояние входа (state, nonce, PKCE) в cookie bs_auth и ведёт в Keycloak.
func (s *Server) startLogin(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := s.oidc.Available(r.Context()); err != nil {
		s.log.WarnContext(r.Context(), "провайдер входа временно недоступен")
		w.Header().Set("Retry-After", "5")
		s.renderLogin(w, r, http.StatusServiceUnavailable, "Служба входа временно недоступна. Повторите вход позже.", s.safeNext(q.Get("next")))
		return
	}
	st, err := oidc.NewLoginState(s.safeNext(q.Get("next")))
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Вход недоступен", "Не удалось начать вход. Повторите позже.")
		return
	}
	if err := s.login.Save(w, r, st, s.clock.Now().Add(loginStateTTL)); err != nil {
		s.log.ErrorContext(r.Context(), "состояние входа не сохранено", "err", err)
		s.renderError(w, r, http.StatusInternalServerError, "Вход недоступен", "Не удалось начать вход. Повторите позже.")
		return
	}
	hint := q.Get("hint")
	if !loginHintPattern.MatchString(hint) {
		hint = ""
	}
	// Адрес строится из discovery Keycloak; от пользователя в нём только login_hint по белому шаблону.
	//nolint:gosec // G710: перенаправление только на authorization endpoint провайдера из конфигурации
	http.Redirect(w, r, s.oidc.AuthURL(st, hint), http.StatusFound)
}

// callback — возврат из Keycloak: state сверяется с cookie, код обменивается с PKCE verifier,
// id_token проверяется с nonce, сессия создаётся с новым CSRF-токеном.
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("error") != "" {
		s.loginFailed(w, r, "provider_error", msgCancelled)
		return
	}
	var st session.LoginState
	_, err := s.login.Load(r, &st)
	// Состояние входа одноразовое: удаляется при любом исходе.
	s.login.Clear(w, r)
	if err != nil {
		s.loginFailed(w, r, "no_login_state", msgRetry)
		return
	}
	if !session.Equal(q.Get("state"), st.State) {
		s.loginFailed(w, r, "state", msgRetry)
		return
	}
	res, err := s.oidc.Exchange(r.Context(), q.Get("code"), st)
	if err != nil {
		s.log.InfoContext(r.Context(), "вход не подтверждён", "err", err)
		reason, msg := "exchange", msgFailed
		switch {
		case errors.Is(err, oidc.ErrNonce):
			reason = "nonce"
		case errors.Is(err, oidc.ErrPersonalData):
			reason, msg = "personal_data", msgPersonalData
		}
		s.loginFailed(w, r, reason, msg)
		return
	}
	csrf, err := session.RandomToken()
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "Вход недоступен", "Не удалось начать сессию. Повторите позже.")
		return
	}
	d := applyResult(session.Data{CSRF: csrf}, res)
	if err := s.saveSession(w, r, d, s.clock.Now().Add(s.cfg.SessionTTL)); err != nil {
		s.log.ErrorContext(r.Context(), "сессия не сохранена", "err", err)
		s.renderError(w, r, http.StatusInternalServerError, "Вход недоступен", "Не удалось начать сессию. Повторите позже.")
		return
	}
	p := d.Principal()
	s.record(r, auditclient.Entry{Action: auditc.ActionLogin, ObjectType: "session", ObjectID: p.Username,
		Result: auditc.ResultSuccess, Principal: &p})
	http.Redirect(w, r, s.safeNext(st.Next), http.StatusSeeOther)
}

// loginFailed пишет неудачный вход в аудит и показывает страницу входа с объяснением.
func (s *Server) loginFailed(w http.ResponseWriter, r *http.Request, reason, msg string) {
	s.record(r, auditclient.Entry{Action: auditc.ActionLogin, ObjectType: "session", ObjectID: "anonymous",
		Result: auditc.ResultDenied, Details: map[string]any{"reason": reason}})
	s.renderLogin(w, r, http.StatusBadRequest, msg, "/")
}

// logout — только POST с CSRF (проверен в middleware): аудит, удаление cookie, end-session Keycloak.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if p, ok := auth.FromContext(r.Context()); ok {
		s.record(r, auditclient.Entry{Action: auditc.ActionLogout, ObjectType: "session", ObjectID: p.Username,
			Result: auditc.ResultSuccess, Principal: &p})
	}
	s.sessions.Clear(w, r)
	http.Redirect(w, r, s.oidc.LogoutURL(), http.StatusSeeOther)
}

// home — стартовая страница после входа (экраны ролей подключаются пакетами UI).
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	s.renderScreen(w, r, http.StatusOK, "home.html", "Главная", struct{}{})
}

// forbidden — страница 403 для authz.Guard.
func (s *Server) forbidden(w http.ResponseWriter, r *http.Request) {
	s.renderError(w, r, http.StatusForbidden, "Недостаточно прав",
		"Операция недоступна вашей роли или клинике. Если доступ нужен по работе, обратитесь в ИТ-службу.")
}
