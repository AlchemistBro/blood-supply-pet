// auditverify вызывает проверку цепочки хешей журнала аудита на поднятом стенде и завершается
// с ненулевым кодом, если цепочка нарушена. Токен берётся у Keycloak пользователем дежурного (демо).
//
//	go run ./tools/auditverify
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	os.Exit(run())
}

func run() int {
	keycloak := flag.String("keycloak", "http://localhost:8180", "адрес Keycloak")
	audit := flag.String("audit", "http://localhost:8108", "адрес сервиса audit")
	user := flag.String("user", "duty1", "пользователь с правом чтения журнала")
	password := flag.String("password", "demo", "пароль демо-пользователя")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	token, err := login(ctx, *keycloak, *user, *password)
	if err != nil {
		return fail(err)
	}
	body, status, err := get(ctx, *audit+"/internal/verify", token)
	if err != nil {
		return fail(fmt.Errorf("сервис audit недоступен: %w", err))
	}
	if status != http.StatusOK {
		return fail(fmt.Errorf("verify вернул %d: %s", status, body))
	}
	var r struct {
		OK      bool  `json:"ok"`
		Checked int64 `json:"checked"`
		Broken  *struct {
			Seq    int64  `json:"seq"`
			Reason string `json:"reason"`
		} `json:"broken"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return fail(err)
	}
	if !r.OK && r.Broken != nil {
		fmt.Printf("ЦЕПОЧКА НАРУШЕНА на записи %d: %s (проверено до разрыва: %d)\n", r.Broken.Seq, r.Broken.Reason, r.Checked)
		return 1
	}
	fmt.Printf("цепочка аудита цела, проверено записей: %d\n", r.Checked)
	return 0
}

func get(ctx context.Context, target, token string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return do(req)
}

func do(req *http.Request) ([]byte, int, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}

func login(ctx context.Context, keycloak, user, password string) (string, error) {
	form := url.Values{"grant_type": {"password"}, "client_id": {"blood-supply-cli"}, "username": {user}, "password": {password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, keycloak+"/realms/blood-supply/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body, status, err := do(req)
	if err != nil {
		return "", fmt.Errorf("сервис Keycloak недоступен: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("вход %s: %d %s", user, status, body)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("токен не получен: %w", err)
	}
	if tr.AccessToken == "" {
		return "", errors.New("токен не получен: пустой ответ")
	}
	return tr.AccessToken, nil
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "ошибка:", err)
	return 2
}
