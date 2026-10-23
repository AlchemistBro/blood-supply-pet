// Package aistclient — клиент АИСТ-М (C01, ИНТ-01) по HTTP: опрос изменений и точечный статус единиц.
// Вызовы идут через httpx.Client с таймаутом и предохранителем (ADR-0004); сетевые вызовы — только вне транзакций.
package aistclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/httpx"
	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
)

// MaxStatusIDs — ограничение АИСТ-М на число кодов в одном запросе статуса.
const MaxStatusIDs = 1000

const maxResponseBytes = 32 << 20

// Client реализует app.AistClient.
type Client struct {
	base    string
	changes *httpx.Client
	status  *httpx.Client
}

var _ app.AistClient = (*Client)(nil)

// Options — таймауты вызовов.
type Options struct {
	// StatusTimeout — таймаут точечного статуса (ADR-0004: 3 с).
	StatusTimeout time.Duration
	// ChangesTimeout — таймаут страницы изменений.
	ChangesTimeout time.Duration
}

// New создаёт клиента. У опроса и точечного статуса свои предохранители: отказ опроса
// не должен мешать ответу transfer, и наоборот.
func New(baseURL string, o Options) *Client {
	if o.StatusTimeout <= 0 {
		o.StatusTimeout = 3 * time.Second
	}
	if o.ChangesTimeout <= 0 {
		o.ChangesTimeout = 10 * time.Second
	}
	return &Client{
		base:    strings.TrimRight(baseURL, "/"),
		changes: httpx.NewClient(httpx.ClientConfig{Name: "aist-changes", Timeout: o.ChangesTimeout, Retries: 1}),
		// Без повторов: весь бюджет вызова transfer → inventory — 3 с.
		status: httpx.NewClient(httpx.ClientConfig{Name: "aist-status", Timeout: o.StatusTimeout}),
	}
}

type unitDTO struct {
	AistUnitCode     string    `json:"aist_unit_code"`
	ClinicCode       string    `json:"clinic_code"`
	ComponentKind    string    `json:"component_kind"`
	BloodGroup       string    `json:"blood_group"`
	Phenotype        string    `json:"phenotype"`
	VolumeML         int64     `json:"volume_ml"`
	ExpiresAt        time.Time `json:"expires_at"`
	StorageLocation  string    `json:"storage_location"`
	Status           string    `json:"status"`
	WithdrawalReason string    `json:"withdrawal_reason"`
	Version          int64     `json:"version"`
	ChangedAt        time.Time `json:"changed_at"`
}

type changesDTO struct {
	Units          []unitDTO `json:"units"`
	LastVersion    int64     `json:"last_version"`
	CurrentVersion int64     `json:"current_version"`
	HasMore        bool      `json:"has_more"`
}

// Changes — GET /units/changes?since=&limit=.
func (c *Client) Changes(ctx context.Context, since int64, limit int) (app.ChangesPage, error) {
	q := url.Values{"since": {strconv.FormatInt(since, 10)}, "limit": {strconv.Itoa(limit)}}
	var dto changesDTO
	if err := c.get(ctx, c.changes, "/units/changes?"+q.Encode(), &dto); err != nil {
		return app.ChangesPage{}, err
	}
	page := app.ChangesPage{LastVersion: dto.LastVersion, CurrentVersion: dto.CurrentVersion, HasMore: dto.HasMore,
		Units: make([]app.AistUnit, 0, len(dto.Units))}
	for _, u := range dto.Units {
		page.Units = append(page.Units, app.AistUnit(u))
	}
	return page, nil
}

type statusDTO struct {
	Units []struct {
		AistUnitCode     string    `json:"aist_unit_code"`
		Status           string    `json:"status"`
		WithdrawalReason string    `json:"withdrawal_reason"`
		Version          int64     `json:"version"`
		ChangedAt        time.Time `json:"changed_at"`
	} `json:"units"`
	NotFound  []string  `json:"not_found"`
	CheckedAt time.Time `json:"checked_at"`
}

// Statuses — GET /units/status?ids=…, пачками не больше MaxStatusIDs. Отказ любой пачки — отказ всего вызова:
// частичный ответ из источника хуже честного ответа из копии.
func (c *Client) Statuses(ctx context.Context, codes []string) (app.StatusPage, error) {
	var out app.StatusPage
	for start := 0; start < len(codes); start += MaxStatusIDs {
		end := min(start+MaxStatusIDs, len(codes))
		var dto statusDTO
		q := url.Values{"ids": {strings.Join(codes[start:end], ",")}}
		if err := c.get(ctx, c.status, "/units/status?"+q.Encode(), &dto); err != nil {
			return app.StatusPage{}, err
		}
		for _, u := range dto.Units {
			out.Units = append(out.Units, app.AistStatus{
				AistUnitCode: u.AistUnitCode, Status: u.Status, WithdrawalReason: u.WithdrawalReason,
				Version: u.Version, ChangedAt: u.ChangedAt.UTC(),
			})
		}
		out.NotFound = append(out.NotFound, dto.NotFound...)
		if dto.CheckedAt.After(out.CheckedAt) {
			out.CheckedAt = dto.CheckedAt.UTC()
		}
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, hc *httpx.Client, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return fmt.Errorf("запрос АИСТ-М: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("АИСТ-М недоступна: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("АИСТ-М ответила %d на %s", resp.StatusCode, strings.SplitN(path, "?", 2)[0])
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(v); err != nil {
		return fmt.Errorf("разбор ответа АИСТ-М: %w", err)
	}
	return nil
}
