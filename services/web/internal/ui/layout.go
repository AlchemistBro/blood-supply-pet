package ui

import "github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"

// Page — данные полной страницы: макет плюс данные экрана в Content.
// Шаблон экрана определяет "content" (и при желании "page_actions") и получает всю Page, поэтому видит CSRF-токен.
type Page struct {
	Title       string // заголовок h1 и <title>
	Header      Header
	Nav         []NavItem    // меню роли; собирает вызывающий по матрице прав
	Breadcrumbs []Breadcrumb // последняя крошка — текущая страница, без URL
	Alerts      []Alert      // полосы под шапкой сверх водяного знака: «АИСТ-М недоступна…»
	Flashes     []Flash      // уведомления после POST/redirect (TakeFlashes)
	Footer      Footer
	CSRFToken   string // токен сессии: поле csrf_token в формах и заголовок X-CSRF-Token для htmx (ADR-0018)
	Content     any
}

// Header — шапка: роль, клиника, логин и водяной знак запаса (NFR015).
type Header struct {
	User      User
	Clinic    string // название клиники; пусто — роль без привязки к клинике (аналитик, дежурный, ИТ-служба)
	Freshness Freshness
	HomeURL   string // по умолчанию "/"
	LogoutURL string // POST; по умолчанию "/logout"
}

// User — то, что шапка знает о пользователе: логин (preferred_username) и роль.
// Полей для ФИО нет намеренно: в системе нет ФИО и иных идентификаторов лиц (правила канона).
type User struct {
	Login string
	Role  auth.Role
}

// SignedIn — пользователь вошёл; без входа шапка не показывает роль, клинику и водяной знак.
func (h Header) SignedIn() bool { return h.User.Login != "" }

// ClinicText — клиника для шапки; у ролей без клиники — «Все клиники».
func (h Header) ClinicText() string {
	if h.Clinic == "" {
		return "Все клиники"
	}
	return h.Clinic
}

// Home — адрес эмблемы и названия системы.
func (h Header) Home() string {
	if h.HomeURL == "" {
		return "/"
	}
	return h.HomeURL
}

// Logout — адрес формы выхода (только POST, ADR-0018).
func (h Header) Logout() string {
	if h.LogoutURL == "" {
		return "/logout"
	}
	return h.LogoutURL
}

// NavItem — пункт левой навигации.
type NavItem struct {
	Label  string
	URL    string
	Active bool
	Count  int // число требующих внимания (задания в ожидании); 0 — не показывается
}

// Breadcrumb — хлебная крошка; пустой URL — текущая страница.
type Breadcrumb struct {
	Label string
	URL   string
}

// Alert — полоса предупреждения на всю ширину под шапкой.
type Alert struct {
	Tone Tone
	Text string
}

// Flash — уведомление о результате действия, показывается один раз после перенаправления.
type Flash struct {
	Tone Tone
	Text string
}

// Footer — подвал: версия сборки и ссылка на исходный код. Пометка «демо» выводится всегда.
type Footer struct {
	Version string
	RepoURL string
}

// LiveRole — роль ARIA для сообщения: опасное зачитывается сразу (alert), прочее — вежливо (status).
func (t Tone) LiveRole() string {
	if t == ToneDanger {
		return "alert"
	}
	return "status"
}
