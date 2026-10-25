package server

import (
	"encoding/csv"
	"net/http"
	"net/url"
	"strconv"
	"time"

	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/auditclient"
)

type auditRecordView struct {
	Seq        int64     `json:"seq"`
	At         time.Time `json:"occurred_at"`
	Module     string    `json:"module"`
	Action     string    `json:"action"`
	ObjectType string    `json:"object_type"`
	ObjectID   string    `json:"object_id"`
	User       string    `json:"user_id"`
	Result     string    `json:"result"`
	Hash       string    `json:"hash"`
}
type auditVerification struct {
	OK      bool  `json:"ok"`
	Checked int64 `json:"checked"`
	Broken  *struct {
		Seq    int64  `json:"seq"`
		Reason string `json:"reason"`
	} `json:"broken"`
}
type auditScreen struct {
	Items              []auditRecordView `json:"items"`
	Next               *int64            `json:"next_after_seq"`
	NextURL, ExportURL string
	Filters            url.Values
	Verification       *auditVerification
}

func (s *Server) registerAudit(mux *http.ServeMux) {
	mux.Handle("GET /audit", s.guard.Require(auth.OpReadAudit)(http.HandlerFunc(s.auditPage)))
	export := s.guard.Require(auth.OpReadAudit)(http.HandlerFunc(s.auditCSV))
	mux.Handle("GET /audit/export.csv", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.FromContext(r.Context())
		result := auditc.ResultDenied
		if auth.Allowed(auth.OpReadAudit, p.Roles) {
			result = auditc.ResultSuccess
		}
		s.record(r, auditclient.Entry{Action: "AUDIT_EXPORT", ObjectType: "audit_record", ObjectID: "page", Result: result, Principal: &p})
		export.ServeHTTP(w, r)
	}))
	mux.Handle("POST /audit/verify", s.guard.Require(auth.OpReadAudit)(http.HandlerFunc(s.auditVerify)))
}
func auditQuery(r *http.Request) url.Values {
	q := url.Values{"limit": {"100"}}
	for _, key := range []string{"module", "action", "object_type", "object_id", "user_id", "from", "to", "after_seq"} {
		if value := r.URL.Query().Get(key); value != "" {
			q.Set(key, value)
		}
	}
	return q
}

// auditAPIQuery переименовывает ключи формы в имена параметров API журнала (camelCase): иначе сервис молча
// игнорирует фильтры по объекту, учётной записи и курсор страницы.
func auditAPIQuery(q url.Values) url.Values {
	rename := map[string]string{"object_type": "objectType", "object_id": "objectId", "user_id": "userId", "after_seq": "afterSeq"}
	out := url.Values{}
	for key, values := range q {
		if name, ok := rename[key]; ok {
			key = name
		}
		out[key] = values
	}
	return out
}
func (s *Server) auditPage(w http.ResponseWriter, r *http.Request) {
	q := auditQuery(r)
	data := auditScreen{Filters: q, ExportURL: "/audit/export.csv?" + q.Encode()}
	if err := s.auditReader.GetJSON(r.Context(), "/records?"+auditAPIQuery(q).Encode(), &data); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	if data.Next != nil {
		q.Set("after_seq", strconv.FormatInt(*data.Next, 10))
		data.NextURL = "/audit?" + q.Encode()
	}
	s.renderScreen(w, r, 200, "audit.html", "Журнал аудита", data)
}
func (s *Server) auditVerify(w http.ResponseWriter, r *http.Request) {
	var result auditVerification
	if err := s.auditReader.GetJSON(r.Context(), "/internal/verify", &result); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	s.renderScreen(w, r, 200, "audit.html", "Проверка журнала аудита", auditScreen{Filters: url.Values{}, Verification: &result})
}

// auditCSV выгружает одну страницу: курсор исключает неограниченный ответ и удержание памяти BFF.
func (s *Server) auditCSV(w http.ResponseWriter, r *http.Request) {
	var data auditScreen
	if err := s.auditReader.GetJSON(r.Context(), "/records?"+auditAPIQuery(auditQuery(r)).Encode(), &data); err != nil {
		s.demandFailure(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="audit.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	writer := csv.NewWriter(w)
	if err := writer.Write([]string{"seq", "occurred_at", "module", "action", "object_type", "object_id", "user_id", "result", "hash"}); err != nil {
		return
	}
	for _, item := range data.Items {
		row := []string{strconv.FormatInt(item.Seq, 10), item.At.UTC().Format(time.RFC3339Nano), item.Module, item.Action, item.ObjectType, item.ObjectID, item.User, item.Result, item.Hash}
		for i, cell := range row {
			row[i] = csvCell(cell)
		}
		if err := writer.Write(row); err != nil {
			return
		}
	}
	writer.Flush()
}

// csvCell не даёт табличному редактору исполнять формулу из поля журнала.
func csvCell(value string) string {
	if len(value) > 0 {
		switch value[0] {
		case '=', '+', '-', '@', '\t', '\r', '\n':
			return "'" + value
		}
	}
	return value
}
