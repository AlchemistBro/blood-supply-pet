// notification — письма по SMTP (C04, ИНТ-04): срочное задание оператору клиники-источника (FR044), эскалации
// и нарушения обязательства — дежурному смены. Читает transfer.events, адресаты — статический справочник (ADR-0021).
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/libs/runtime"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/notification/api"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/adapters/smtpmail"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/api"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/letters"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/recipients"
	"github.com/AlchemistBro/blood-supply-pet/services/notification/migrations"
)

// config — переменные окружения сервиса (общие BS_* читает libs/runtime).
type config struct {
	SMTPAddr       string        `env:"BS_SMTP_ADDR" envDefault:"localhost:1025"`
	SMTPFrom       string        `env:"BS_SMTP_FROM" envDefault:"no-reply@demo.blood-supply.local"`
	SMTPTimeout    time.Duration `env:"BS_SMTP_TIMEOUT" envDefault:"10s"`
	SMTPStartTLS   bool          `env:"BS_SMTP_STARTTLS" envDefault:"false"`
	WebBaseURL     string        `env:"BS_WEB_BASE_URL" envDefault:"http://localhost:8080"`
	RecipientsFile string        `env:"BS_NOTIFY_RECIPIENTS_FILE"`
	SendInterval   time.Duration `env:"BS_NOTIFY_INTERVAL" envDefault:"5s"`
	MaxAttempts    int           `env:"BS_NOTIFY_MAX_ATTEMPTS" envDefault:"12"`
}

func main() {
	runtime.Run(runtime.Spec{
		Name:       "notification",
		Module:     "notification",
		Migrations: migrations.FS,
		OpenAPI:    apispec.Spec,
		Setup:      setup,
	})
}

func setup(ctx context.Context, rt *runtime.Runtime) ([]runtime.Worker, error) {
	var cfg config
	if err := env.Parse(&cfg); err != nil {
		return nil, fmt.Errorf("конфигурация notification: %w", err)
	}
	dir, err := recipients.LoadFile(cfg.RecipientsFile, rt.Refdata.Clinics)
	if err != nil {
		return nil, err
	}
	renderer, err := letters.New(rt.Refdata, cfg.WebBaseURL)
	if err != nil {
		return nil, err
	}
	mailer, err := smtpmail.New(smtpmail.Config{
		Addr: cfg.SMTPAddr, From: cfg.SMTPFrom, Timeout: cfg.SMTPTimeout, StartTLS: cfg.SMTPStartTLS,
	}, rt.Clock)
	if err != nil {
		return nil, err
	}
	appCfg := app.DefaultConfig()
	appCfg.Lease = max(appCfg.Lease, cfg.SMTPTimeout+30*time.Second)
	appCfg.Interval = cfg.SendInterval
	if cfg.MaxAttempts > 0 {
		appCfg.Retry.MaxAttempts = cfg.MaxAttempts
	}
	svc := app.New(app.Deps{
		Pool: rt.Pool, Store: postgres.New(rt.Pool), Directory: dir, Letters: renderer, Mailer: mailer,
		Journal: rt.Journal, Clock: rt.Clock, Log: rt.Log,
	}, appCfg)
	api.Mount(rt.Mux, svc, rt.Deny())

	workers := []runtime.Worker{svc.RunSender}
	if len(rt.Cfg.KafkaBrokers) > 0 {
		topics := []string{contracts.TopicTransferEvents, contracts.TopicInventoryEvents, contracts.TopicSecurityEvents}
		if err := messaging.EnsureTopics(ctx, rt.Cfg.KafkaBrokers, 1, messaging.WithDLT(topics...)...); err != nil {
			return nil, err
		}
		consumer, err := messaging.NewConsumer(messaging.ConsumerConfig{
			Brokers: rt.Cfg.KafkaBrokers, Group: "notification", Topics: topics,
		}, svc.HandleMessage, rt.Log)
		if err != nil {
			return nil, err
		}
		workers = append(workers, consumer.Run)
	}
	return workers, nil
}
