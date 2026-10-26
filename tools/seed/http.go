package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
)

type statusError struct{ Code int }

var errStandTimeout = errors.New("превышено время ожидания ответа стенда")

func (e statusError) Error() string { return fmt.Sprintf("HTTP %d", e.Code) }
func (s seeder) call(method, address, token, kind string, body io.Reader, out any) error {
	request, err := http.NewRequestWithContext(s.ctx, method, address, body)
	if err != nil {
		return err
	}
	if kind != "" {
		request.Header.Set("Content-Type", kind)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := s.http.Do(request)
	if err != nil {
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return errStandTimeout
		}
		return errors.New("соединение со стендом не установлено")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return statusError{response.StatusCode}
	}
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 16<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(out)
}
func (s seeder) json(method, address, token string, body, out any) error {
	var input io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input = bytes.NewReader(raw)
	}
	return s.call(method, address, token, "application/json", input, out)
}
func (s seeder) login(user string) (string, error) {
	return s.token(url.Values{"grant_type": {"password"}, "client_id": {"blood-supply-cli"}, "username": {user}, "password": {setting("BS_SEED_PASSWORD", "demo")}})
}
func (s seeder) token(form url.Values) (string, error) {
	var result struct {
		Token string `json:"access_token"`
	}
	err := s.call(http.MethodPost, strings.TrimRight(s.keycloak, "/")+"/realms/blood-supply/protocol/openid-connect/token", "", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), &result)
	if err != nil {
		return "", fmt.Errorf("вход в Keycloak: %w", err)
	}
	if result.Token == "" {
		return "", errors.New("keycloak не выдал токен")
	}
	return result.Token, nil
}
func (s seeder) file(token, kind string, file testdata.CSVFile) (uuid.UUID, error) {
	var existing struct {
		Items []struct {
			ID     uuid.UUID `json:"id"`
			Hash   string    `json:"file_checksum"`
			Status string    `json:"check_status"`
		} `json:"items"`
	}
	if err := s.json(http.MethodGet, s.demand+"/files?limit=500&file_kind="+url.QueryEscape(kind), token, nil, &existing); err != nil {
		return uuid.Nil, err
	}
	checksum := sha256.Sum256(file.Content)
	hash := hex.EncodeToString(checksum[:])
	for _, item := range existing.Items {
		if item.Hash == hash && (item.Status == "ACCEPTED" || item.Status == "PARTIAL") {
			return item.ID, nil
		}
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("file_kind", kind); err != nil {
		return uuid.Nil, err
	}
	part, err := writer.CreateFormFile("file", file.Name)
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := part.Write(file.Content); err != nil {
		return uuid.Nil, err
	}
	if err := writer.Close(); err != nil {
		return uuid.Nil, err
	}
	var result struct {
		File struct {
			ID     uuid.UUID `json:"id"`
			Status string    `json:"check_status"`
		} `json:"file"`
	}
	if err := s.call(http.MethodPost, s.demand+"/files", token, writer.FormDataContentType(), &body, &result); err != nil {
		return uuid.Nil, fmt.Errorf("CSV %s: %w", kind, err)
	}
	// Часть строк истории расхода отклоняется, если за эти периоды данные уже есть (повторный запуск на стенде
	// с загруженной вручную историей). Оценка берёт всю существующую историю прошлого года, поэтому это не ошибка.
	historyOverlap := kind == "CONSUMPTION_HISTORY" && result.File.Status == "PARTIAL"
	if result.File.Status != "ACCEPTED" && !historyOverlap {
		return uuid.Nil, fmt.Errorf("CSV %s не принят целиком", kind)
	}
	return result.File.ID, nil
}
func (s seeder) waitPlan(token string, id, forecast, estimate uuid.UUID) (uuid.UUID, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	base := id
	for attempts := 0; attempts < 120; attempts++ {
		var built struct {
			Demand uuid.UUID `json:"demand_estimate_id"`
		}
		err := s.json(http.MethodPost, s.planning+"/plans", token, map[string]any{"idempotency_key": id, "emergency_forecast_id": forecast}, &built)
		if err == nil && built.Demand == estimate {
			return id, nil
		}
		var status statusError
		if err != nil && (!errors.As(err, &status) || (status.Code != 422 && status.Code != 409 && status.Code != 503)) {
			return uuid.Nil, fmt.Errorf("построение плана: %w", err)
		}
		if err == nil {
			id = uuid.NewSHA1(base, []byte(fmt.Sprint(attempts)))
		}
		select {
		case <-s.ctx.Done():
			return uuid.Nil, s.ctx.Err()
		case <-ticker.C:
		}
	}
	return uuid.Nil, errors.New("проекции planning не подготовились за отведённое время")
}

// waitSync не принимает пропущенный из-за фонового опроса сеанс за готовность данных.
func (s seeder) waitSync(token string) error {
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
	defer cancel()
	s.ctx = ctx
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		var result struct {
			Result  string `json:"result"`
			Skipped bool   `json:"skipped"`
		}
		err := s.json(http.MethodPost, s.inventory+"/clinics/1/sync", token, struct{}{}, &result)
		if err == nil && !result.Skipped {
			if result.Result != "SUCCESS" {
				return errors.New("АИСТ-М не синхронизирован целиком")
			}
			return nil
		}
		var status statusError
		if err != nil && !errors.Is(err, errStandTimeout) && (!errors.As(err, &status) || (status.Code != 409 && status.Code != 503)) {
			return err
		}
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-ticker.C:
		}
	}
}

// seedNorms can safely resume a timed-out bulk request: the server only fills
// missing norms and retains committed progress and manually entered values.
func (s seeder) seedNorms(token string, year int) error {
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
	defer cancel()
	s.ctx = ctx
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		err := s.json(http.MethodPost, s.inventory+"/dev/seed-norms", token, map[string]any{"consumption_year": year}, nil)
		if err == nil || !errors.Is(err, errStandTimeout) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
