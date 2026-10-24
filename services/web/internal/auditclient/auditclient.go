// Package auditclient — запись аудита web в сервис audit по HTTP (ADR-0017): POST /records служебным токеном
// (роль SERVICE, client credentials). У web нет базы и outbox, поэтому это единственный его путь в журнал.
package auditclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/correlation"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
)

// TokenSource — источник служебного токена. Реализация — auth.ServiceToken (client credentials).
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Entry — запись аудита от web.
type Entry struct {
	Action     string
	ObjectType string
	ObjectID   string
	Result     string
	// Principal — кто действовал; nil для анонимного запроса.
	Principal *auth.Principal
	// Details — без ПДн и номера случая (проверяется до отправки).
	Details map[string]any
}

// Recorder пишет запись аудита.
type Recorder interface {
	Record(ctx context.Context, e Entry) error
}

// forbiddenDetails — ключи, которые audit отклоняет (ADR-0017); web не отправляет их вовсе.
var forbiddenDetails = map[string]bool{
	"patient_case_no": true, "full_name": true, "snils": true, "phenotype": true, "password": true,
}

// Client — HTTP-клиент сервиса audit.
type Client struct {
	url    string
	http   *httpx.Client
	tokens TokenSource
	clock  clock.Clock
}

// New создаёт клиента. baseURL — адрес audit (http://audit:8080).
func New(baseURL string, tokens TokenSource, clk clock.Clock) *Client {
	return &Client{
		url: strings.TrimRight(baseURL, "/") + "/records",
		// POST не повторяется: повтор записи дал бы дубль в журнале.
		http:   httpx.NewClient(httpx.ClientConfig{Name: "audit", Timeout: 3 * time.Second}),
		tokens: tokens, clock: clk,
	}
}

// Record отправляет запись и ждёт 201.
func (c *Client) Record(ctx context.Context, e Entry) error {
	for k := range e.Details {
		if forbiddenDetails[strings.ToLower(k)] {
			return fmt.Errorf("аудит: ключ %q в details запрещён", k)
		}
	}
	rec := auditc.AuditRecorded{
		OccurredAt: c.clock.Now(), Module: auditc.ModuleWeb, Action: e.Action,
		ObjectType: e.ObjectType, ObjectID: e.ObjectID, Result: e.Result, Details: e.Details,
		CorrelationID: correlation.From(ctx).Correlation,
	}
	if e.Principal != nil {
		rec.UserID, rec.Role, rec.ClinicID = e.Principal.Username, e.Principal.PrimaryRole(), e.Principal.ClinicID
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("аудит: %w", err)
	}
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("аудит: служебный токен: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("аудит: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("аудит: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("аудит: ответ %d", resp.StatusCode)
	}
	return nil
}

// LogOnly — запись только в лог процесса: для локальной отладки без сервиса audit (BS_AUDIT_URL не задан).
type LogOnly struct {
	Log *slog.Logger
}

// Record пишет запись в лог и никогда не возвращает ошибку.
func (l LogOnly) Record(ctx context.Context, e Entry) error {
	log := l.Log
	if log == nil {
		log = slog.Default()
	}
	user := ""
	if e.Principal != nil {
		user = e.Principal.Username
	}
	log.WarnContext(ctx, "аудит не настроен, запись только в лог",
		"action", e.Action, "object_type", e.ObjectType, "object_id", e.ObjectID, "result", e.Result, "user_id", user)
	return nil
}
