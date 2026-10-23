// Команда healthcheck — проверка здоровья контейнера для docker compose. Образ сервиса собран на distroless:
// в нём нет ни shell, ни curl, поэтому healthcheck вызывает этот маленький бинарник:
//
//	healthcheck http://127.0.0.1:8080/healthz
//
// Код выхода 0 — ответ 200 за 2 секунды, иначе 1.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "использование: healthcheck <url>")
		return 2
	}
	if err := check(args[0], 2*time.Second); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func check(url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("запрос %s: %w", url, err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // адрес задаёт healthcheck в compose
	if err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s ответил %d", url, resp.StatusCode)
	}
	return nil
}
