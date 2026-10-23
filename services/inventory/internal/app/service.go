package app

import (
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
)

// ErrInvalid — запрос не проходит проверку (400).
var ErrInvalid = errors.New("некорректный запрос")

// Config — настройки сценариев.
type Config struct {
	// PollInterval — период опроса изменений АИСТ-М (BS_AIST_POLL_INTERVAL).
	PollInterval time.Duration
	// PageLimit — размер страницы изменений (АИСТ-М: по умолчанию 500, не больше 5000).
	PageLimit int
	// MaxPagesPerRun — сколько страниц за один сеанс; остальное догонит следующий.
	MaxPagesPerRun int
	// StatusTimeout — общий бюджет точечного статуса (ADR-0004: 3 с).
	StatusTimeout time.Duration
	// MaxStatusIDs — сколько единиц можно спросить за один вызов.
	MaxStatusIDs int
}

func (c Config) withDefaults() Config {
	if c.PollInterval <= 0 {
		c.PollInterval = 5 * time.Second
	}
	if c.PageLimit <= 0 {
		c.PageLimit = 500
	}
	if c.MaxPagesPerRun <= 0 {
		c.MaxPagesPerRun = 100
	}
	if c.StatusTimeout <= 0 {
		c.StatusTimeout = 3 * time.Second
	}
	if c.MaxStatusIDs <= 0 {
		c.MaxStatusIDs = 1000
	}
	return c
}

// Deps — зависимости сервиса.
type Deps struct {
	Pool      *pgxpool.Pool
	Store     Store
	Aist      AistClient
	Publisher Publisher
	Journal   *messaging.Journal
	Ref       *refdata.Set
	Clock     clock.Clock
	Log       *slog.Logger
	Config    Config
}

// Service — сценарии inventory.
type Service struct {
	pool    *pgxpool.Pool
	store   Store
	aist    AistClient
	pub     Publisher
	journal *messaging.Journal
	ref     *refdata.Set
	cat     *catalog
	clock   clock.Clock
	log     *slog.Logger
	cfg     Config
}

// New собирает сервис.
func New(d Deps) *Service {
	return &Service{
		pool: d.Pool, store: d.Store, aist: d.Aist, pub: d.Publisher, journal: d.Journal, ref: d.Ref,
		cat: newCatalog(d.Ref), clock: d.Clock, log: d.Log, cfg: d.Config.withDefaults(),
	}
}

// MaxStatusIDs — ограничение числа единиц в одном запросе статуса.
func (s *Service) MaxStatusIDs() int { return s.cfg.MaxStatusIDs }
