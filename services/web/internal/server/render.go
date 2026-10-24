package server

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strconv"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

// Минимальные шаблоны входа, главной и ошибок. Без встроенных скриптов и стилей (CSP, ADR-0018);
// оформление по docs/ui/style-guide.md подключит UI-кит.
//
//go:embed templates/*.html templates/screens/*.html
var templateFS embed.FS

type pages struct {
	byName map[string]*template.Template
}

func loadPages() (*pages, error) {
	p := &pages{byName: map[string]*template.Template{}}
	for _, name := range []string{"login", "home", "error"} {
		t, err := template.New(name).ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("шаблон %s: %w", name, err)
		}
		p.byName[name] = t
	}
	return p, nil
}

type demoUser struct {
	Login string
	Label string
}

// userView — шапка: только логин, роль и клиника; ФИО в системе нет (ADR-0018).
type userView struct {
	Login  string
	Role   string
	Clinic string
}

type view struct {
	Title   string
	User    *userView
	CSRF    string
	Error   string
	Heading string
	Message string
	Next    string
	Demo    []demoUser
	Version string
}

var roleNames = map[auth.Role]string{
	auth.RolePhysician: "Врач-трансфузиолог",
	auth.RoleOperator:  "Оператор клиники",
	auth.RoleAnalyst:   "Территориальный аналитик",
	auth.RoleDuty:      "Дежурный смены",
	auth.RoleITService: "ИТ-служба",
}

func (s *Server) view(r *http.Request, title string) view {
	v := view{Title: title, Version: s.cfg.Version}
	if d, ok := currentSession(r.Context()); ok {
		p := d.Principal()
		u := &userView{Login: p.Username, Role: roleNames[auth.Role(p.PrimaryRole())], Clinic: "Все клиники"}
		if u.Role == "" {
			u.Role = p.PrimaryRole()
		}
		if p.ClinicID != nil {
			u.Clinic = "Клиника № " + strconv.FormatInt(*p.ClinicID, 10)
		}
		v.User, v.CSRF = u, d.CSRF
	}
	return v
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, v view) {
	var buf bytes.Buffer
	if err := s.pages.byName[name].ExecuteTemplate(&buf, "layout", v); err != nil {
		s.log.ErrorContext(r.Context(), "шаблон не отрисован", "page", name, "err", err)
		http.Error(w, "Внутренняя ошибка. Повторите позже.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// renderError — страница ошибки человеческим языком (403, устаревшая форма, недоступность входа).
func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, heading, msg string) {
	v := s.view(r, heading)
	v.Heading, v.Message = heading, msg
	s.render(w, r, status, "error", v)
}
