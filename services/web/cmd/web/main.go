// web — интерфейс и BFF: вход через Keycloak, сессия в зашифрованной cookie, без своей базы (ADR-0007, ADR-0018).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/httpx/auth"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/libs/runtime"
	apispec "github.com/AlchemistBro/blood-supply-pet/services/web/api"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/auditclient"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/live"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/oidc"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/server"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/session"
	"github.com/AlchemistBro/blood-supply-pet/services/web/internal/svcclient"
)

// config — переменные web сверх общих (BS_OIDC_ISSUER, BS_OIDC_AUDIENCE и прочие читает runtime).
type config struct {
	PlanningURL  string `env:"BS_PLANNING_URL" envDefault:"http://planning:8080"`
	DemandURL    string `env:"BS_DEMAND_URL" envDefault:"http://demand:8080"`
	TransferURL  string `env:"BS_TRANSFER_URL" envDefault:"http://transfer:8080"`
	InventoryURL string `env:"BS_INVENTORY_URL" envDefault:"http://inventory:8080"`
	// PublicURL — адрес web, который видит браузер (redirect_uri и возврат после выхода).
	PublicURL string `env:"BS_WEB_PUBLIC_URL" envDefault:"http://localhost:8080"`
	// OIDCDiscoveryURL — адрес realm изнутри сети (compose: http://keycloak:8080/realms/blood-supply).
	OIDCDiscoveryURL   string        `env:"BS_OIDC_DISCOVERY_URL"`
	ClientID           string        `env:"BS_OIDC_CLIENT_ID" envDefault:"web"`
	ClientSecret       string        `env:"BS_OIDC_CLIENT_SECRET,required"`
	SessionKey         string        `env:"BS_SESSION_KEY,required"`
	SessionKeyPrevious string        `env:"BS_SESSION_KEY_PREVIOUS"`
	SessionTTL         time.Duration `env:"BS_SESSION_TTL" envDefault:"8h"`
	CookieSecure       bool          `env:"BS_COOKIE_SECURE" envDefault:"true"`
	HSTSMaxAge         int           `env:"BS_HSTS_MAX_AGE" envDefault:"31536000"`
	DemoAccess         bool          `env:"BS_DEMO_ACCESS" envDefault:"true"`
	// AuditURL — адрес сервиса audit; пусто допустимо только при BS_ENV=local (записи в лог).
	AuditURL string `env:"BS_AUDIT_URL"`
	// Служебный клиент Keycloak для POST /records (роль SERVICE, ADR-0017).
	ServiceClientID     string `env:"BS_SERVICE_CLIENT_ID" envDefault:"services"`
	ServiceClientSecret string `env:"BS_SERVICE_CLIENT_SECRET"`
	// LiveEvents — живые статусы: consumer transfer.events и SSE /events/obligations (нужен BS_KAFKA_BROKERS).
	LiveEvents bool `env:"BS_LIVE_EVENTS" envDefault:"true"`
	// Instance — имя экземпляра для своей consumer-группы web-<имя>; по умолчанию POD_NAME или имя хоста.
	Instance     string        `env:"POD_NAME"`
	SSEHeartbeat time.Duration `env:"BS_SSE_HEARTBEAT" envDefault:"15s"`
}

func main() {
	runtime.Run(runtime.Spec{
		Name:       "web",
		Module:     auditc.ModuleWeb,
		NoDB:       true,
		CustomAuth: true,
		OpenAPI:    apispec.Spec,
		Setup:      setup,
	})
}

func setup(ctx context.Context, rt *runtime.Runtime) ([]runtime.Worker, error) {
	var cfg config
	if err := env.Parse(&cfg); err != nil {
		return nil, fmt.Errorf("конфигурация web: %w", err)
	}
	if rt.Cfg.OIDCIssuer == "" {
		return nil, errors.New("BS_OIDC_ISSUER не задан")
	}
	keys, err := sessionKeys(cfg)
	if err != nil {
		return nil, err
	}
	codec, err := session.NewCodec(rt.Clock, keys...)
	if err != nil {
		return nil, err
	}
	if !cfg.CookieSecure {
		rt.Log.Warn("cookie без атрибута Secure (BS_COOKIE_SECURE=false): только для локальной отладки")
	}

	public := strings.TrimRight(cfg.PublicURL, "/")
	client, err := newOIDCWithRetry(ctx, rt.Log, oidc.Config{
		Issuer: rt.Cfg.OIDCIssuer, DiscoveryURL: cfg.OIDCDiscoveryURL,
		ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
		RedirectURL: public + "/auth/callback", PostLogoutRedirectURL: public + "/login",
		Audience: rt.Cfg.OIDCAudience,
	})
	if err != nil {
		return nil, err
	}

	recorder, err := auditRecorder(cfg, rt)
	if err != nil {
		return nil, err
	}

	srv, err := server.New(server.Config{
		SessionTTL: cfg.SessionTTL, CookieSecure: cfg.CookieSecure, HSTSMaxAge: cfg.HSTSMaxAge,
		DemoAccess: cfg.DemoAccess, Version: runtime.Version,
	}, server.Deps{OIDC: client, Codec: codec, Audit: recorder, Clock: rt.Clock, Log: rt.Log,
		Planning: svcclient.New("planning", cfg.PlanningURL),
		Demand:   svcclient.New("demand", cfg.DemandURL),
		Transfer: svcclient.New("transfer", cfg.TransferURL), Inventory: svcclient.New("inventory", cfg.InventoryURL)})
	if err != nil {
		return nil, err
	}
	srv.Register(rt.Mux)
	// Отказы общих middleware (auth.Require с rt.Deny) тоже уходят в audit по HTTP.
	rt.OnDeny = srv.Guard().Denied()
	rt.Use(srv.Middleware()...)

	workers, err := liveEvents(cfg, rt, srv)
	if err != nil {
		return nil, err
	}
	return workers, nil
}

// liveEvents подключает живые статусы: consumer transfer.events → хаб → SSE. Каждый экземпляр web читает
// топик своей группой (у каждого свои SSE-соединения), inbox не нужен: это только показ.
func liveEvents(cfg config, rt *runtime.Runtime, srv *server.Server) ([]runtime.Worker, error) {
	if !cfg.LiveEvents {
		return nil, nil
	}
	if len(rt.Cfg.KafkaBrokers) == 0 {
		rt.Log.Warn("BS_KAFKA_BROKERS не задан: живые статусы выключены")
		return nil, nil
	}
	instance := cfg.Instance
	if instance == "" {
		host, err := os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("имя экземпляра для группы web: %w", err)
		}
		instance = host
	}
	hub := live.NewHub(64)
	feed := live.NewFeed(hub, rt.Clock, rt.Log)
	consumer, err := messaging.NewConsumer(messaging.ConsumerConfig{
		Brokers: rt.Cfg.KafkaBrokers, Group: "web-" + instance, Topics: []string{contracts.TopicTransferEvents},
		// Только новые события: история для показа не нужна, клиники приходят в самих событиях.
		StartAtEnd: true,
	}, feed.Handle, rt.Log)
	if err != nil {
		return nil, err
	}
	sse := live.NewSSE(hub, srv.Guard(), rt.Log, live.SSEConfig{Heartbeat: cfg.SSEHeartbeat})
	rt.Mux.Handle("GET /events/obligations", sse.Handler())
	return []runtime.Worker{consumer.Run}, nil
}

func sessionKeys(cfg config) ([][]byte, error) {
	current, err := session.ParseKey(cfg.SessionKey)
	if err != nil {
		return nil, fmt.Errorf("BS_SESSION_KEY: %w", err)
	}
	keys := [][]byte{current}
	if cfg.SessionKeyPrevious != "" {
		prev, err := session.ParseKey(cfg.SessionKeyPrevious)
		if err != nil {
			return nil, fmt.Errorf("BS_SESSION_KEY_PREVIOUS: %w", err)
		}
		keys = append(keys, prev)
	}
	return keys, nil
}

// auditRecorder — запись в audit служебным токеном (client credentials); без BS_AUDIT_URL — только лог.
func auditRecorder(cfg config, rt *runtime.Runtime) (auditclient.Recorder, error) {
	if cfg.AuditURL == "" {
		// Без журнала аудита web работает только на машине разработчика (NFR008, ADR-0017).
		if rt.Cfg.Environment != "local" {
			return nil, fmt.Errorf("BS_AUDIT_URL не задан (окружение %q): без аудита web не запускается", rt.Cfg.Environment)
		}
		rt.Log.Warn("BS_AUDIT_URL не задан: вход, выход и отказы пишутся только в лог (только BS_ENV=local)")
		return auditclient.LogOnly{Log: rt.Log}, nil
	}
	if cfg.ServiceClientSecret == "" {
		return nil, errors.New("BS_SERVICE_CLIENT_SECRET не задан: без служебного токена аудит недоступен")
	}
	realm := rt.Cfg.OIDCIssuer
	if cfg.OIDCDiscoveryURL != "" {
		realm = cfg.OIDCDiscoveryURL
	}
	tokens, err := auth.NewServiceToken(auth.ClientCredentialsConfig{
		TokenURL: auth.TokenURL(realm), ClientID: cfg.ServiceClientID, ClientSecret: cfg.ServiceClientSecret,
		Clock: rt.Clock,
	})
	if err != nil {
		return nil, err
	}
	return auditclient.New(cfg.AuditURL, tokens, rt.Clock), nil
}

// newOIDCWithRetry ждёт Keycloak при старте стенда: discovery повторяется до полутора минут.
func newOIDCWithRetry(ctx context.Context, log *slog.Logger, cfg oidc.Config) (*oidc.Client, error) {
	deadline := time.Now().Add(90 * time.Second)
	for {
		c, err := oidc.New(ctx, cfg)
		if err == nil {
			return c, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, err
		}
		log.Warn("Keycloak пока недоступен, повтор через 3 с", "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}
