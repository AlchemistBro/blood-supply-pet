// planning-rebuild восстанавливает собственные проекции, не публикуя заданий (ADR-0034).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/app"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	address := os.Getenv("BS_DB_URL")
	if address == "" {
		return errors.New("задайте BS_DB_URL базы planning с уже применёнными миграциями")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	pool, err := bspgx.Connect(ctx, address)
	if err != nil {
		return err
	}
	defer pool.Close()
	clk := clock.System{}
	svc := app.New(pool, postgres.New(pool), messaging.NewJournal("planning", "planning", clk), refdata.MustLoad(), clk)
	count, err := svc.RebuildProjections(ctx, "planning-rebuild")
	if err != nil {
		return err
	}
	slog.Info("Проекции восстановлены; планы и задания сохранены", "rows", count)
	return nil
}
