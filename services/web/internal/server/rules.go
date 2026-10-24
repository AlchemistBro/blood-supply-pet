package server

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

type ruleView struct {
	ID             uuid.UUID  `json:"matching_rule_id"`
	Code           string     `json:"rule_code"`
	Version        int64      `json:"version_no"`
	Kind           string     `json:"component_kind"`
	RequestedGroup string     `json:"requested_group"`
	AllowedGroup   string     `json:"allowed_group"`
	AllowedKind    string     `json:"allowed_kind"`
	Condition      string     `json:"application_condition"`
	Exception      bool       `json:"is_exception"`
	Priority       int        `json:"priority"`
	Document       string     `json:"basis_document"`
	Clause         string     `json:"basis_clause"`
	From           time.Time  `json:"valid_from"`
	To             *time.Time `json:"valid_to"`
}
type rulesScreen struct {
	Rules     []ruleView
	CanManage bool
}

func (s *Server) rulesPage(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	data := rulesScreen{CanManage: auth.Allowed(auth.OpManageRules, p.Roles)}
	if err := s.matching.GetJSON(r.Context(), "/rules", &data.Rules); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	s.renderScreen(w, r, 200, "rules.html", "Версии правил подбора", data)
}
func (s *Server) addRuleVersion(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if code == "" || len(code) > 64 {
		s.renderError(w, r, 400, "Неверный код", "Укажите код правила.")
		return
	}
	version, err := strconv.ParseInt(r.FormValue("version_no"), 10, 64)
	if err != nil {
		s.renderError(w, r, 400, "Неверная версия", "Введите целое число.")
		return
	}
	priority, err := strconv.Atoi(r.FormValue("priority"))
	if err != nil {
		s.renderError(w, r, 400, "Неверный приоритет", "Введите целое число.")
		return
	}
	from, err := time.Parse("2006-01-02T15:04", r.FormValue("valid_from"))
	if err != nil {
		s.renderError(w, r, 400, "Неверная дата", "Укажите дату начала в UTC.")
		return
	}
	input := map[string]any{"matching_rule_id": uuid.Nil, "rule_code": code, "version_no": version, "component_kind": r.FormValue("component_kind"), "requested_group": r.FormValue("requested_group"), "allowed_group": r.FormValue("allowed_group"), "allowed_kind": r.FormValue("allowed_kind"), "application_condition": r.FormValue("application_condition"), "is_exception": r.FormValue("is_exception") == "true", "priority": priority, "basis_document": r.FormValue("basis_document"), "basis_clause": r.FormValue("basis_clause"), "valid_from": from}
	if value := r.FormValue("valid_to"); value != "" {
		until, err := time.Parse("2006-01-02T15:04", value)
		if err != nil {
			s.renderError(w, r, 400, "Неверная дата", "Укажите дату окончания в UTC.")
			return
		}
		input["valid_to"] = until
	}
	if err := s.matching.PostJSON(r.Context(), "/rules/"+url.PathEscape(code)+"/versions", input, nil); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	http.Redirect(w, r, "/rules", http.StatusSeeOther)
}
