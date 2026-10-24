package server

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/svcclient"
)

type integrationSync struct {
	Watermark *time.Time `json:"watermark"`
	Stale     bool       `json:"is_stale"`
	LastRun   *struct {
		Kind     string     `json:"sync_kind"`
		Started  time.Time  `json:"started_at"`
		Finished *time.Time `json:"finished_at"`
		Result   *string    `json:"sync_result"`
		Error    string     `json:"error_summary"`
	} `json:"last_run"`
}
type integrationTransport struct {
	ID         uuid.UUID `json:"obligation_id"`
	Status     string    `json:"status"`
	Ref        string    `json:"shipment_ref"`
	Error      string    `json:"last_error"`
	Updated    time.Time `json:"updated_at"`
	Dispatched bool      `json:"dispatch_reported"`
	Delivered  bool      `json:"delivery_reported"`
}
type integrationLetter struct {
	ID       uuid.UUID `json:"notification_id"`
	Kind     string    `json:"kind"`
	Role     string    `json:"recipient_role"`
	Status   string    `json:"status"`
	Attempts int       `json:"attempts"`
	Next     time.Time `json:"next_attempt_at"`
	Error    string    `json:"last_error"`
}
type integrationsScreen struct {
	Sync                         integrationSync
	Transport                    []integrationTransport
	Letters                      []integrationLetter
	Errors                       []string
	ObservabilityURL, MailpitURL string
}

func (s *Server) registerIntegrations(mux *http.ServeMux) {
	mux.Handle("GET /integrations", s.guard.Require(auth.OpReadAudit)(http.HandlerFunc(s.integrationsPage)))
}
func (s *Server) integrationsPage(w http.ResponseWriter, r *http.Request) {
	data := integrationsScreen{ObservabilityURL: s.cfg.ObservabilityURL, MailpitURL: s.cfg.MailpitURL}
	var letters struct {
		Items []integrationLetter `json:"items"`
	}
	for _, call := range []struct {
		name, path string
		client     *svcclient.Client
		out        any
	}{
		{"АИСТ-М: состояние обмена", "/internal/sync-status", s.inventory, &data.Sync},
		{"C03: заказы перевозчика", "/internal/transport-status", s.logistics, &data.Transport},
		{"C04: журнал писем", "/notifications?limit=100", s.notification, &letters},
	} {
		if err := call.client.GetJSON(r.Context(), call.path, call.out); err != nil {
			data.Errors = append(data.Errors, call.name)
		}
	}
	data.Letters = letters.Items
	s.renderScreen(w, r, 200, "integrations.html", "Состояние интеграций", data)
}
