package httpx

import (
	"encoding/json"
	"net/http"

	"github.com/AlchemistBro/blood-supply-pet/libs/correlation"
)

// Problem — ошибка в формате RFC 9457 (application/problem+json).
// Title — короткое человеческое название, Detail — что делать пользователю, по-русски.
type Problem struct {
	Type          string         `json:"type,omitempty"`
	Title         string         `json:"title"`
	Status        int            `json:"status"`
	Detail        string         `json:"detail,omitempty"`
	Instance      string         `json:"instance,omitempty"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	Errors        map[string]any `json:"errors,omitempty"`
}

// WriteProblem пишет ошибку с заданным статусом.
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, title, detail string) {
	WriteProblemFull(w, r, Problem{Status: status, Title: title, Detail: detail})
}

// WriteProblemFull пишет ошибку с дополнительными полями.
func WriteProblemFull(w http.ResponseWriter, r *http.Request, p Problem) {
	if p.Status == 0 {
		p.Status = http.StatusInternalServerError
	}
	if p.Instance == "" && r != nil {
		p.Instance = r.URL.Path
	}
	if p.CorrelationID == "" && r != nil {
		p.CorrelationID = correlation.From(r.Context()).Correlation
	}
	if p.Type == "" {
		p.Type = "about:blank"
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// WriteJSON пишет успешный JSON-ответ.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
