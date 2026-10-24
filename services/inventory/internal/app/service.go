package app

import (
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

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
	// StatusTimeout — верхний предел вызова АИСТ-М в точечном статусе (BS_AIST_STATUS_TIMEOUT, 2 с).
	StatusTimeout time.Duration
	// StatusBudget — весь бюджет ответа transfer → inventory, если у входящего запроса нет своего дедлайна
	// (ADR-0004: 3 с).
	StatusBudget time.Duration
	// StatusReserve — запас до дедлайна входящего запроса на ответ из копии (300 мс).
	StatusReserve time.Duration
	// DBTimeout — таймаут чтения копии в точечном статусе.
	DBTimeout time.Duration
	// MaxStatusIDs — сколько единиц можно спросить за один вызов.
	MaxStatusIDs int
	// MaxPollBackoff — предел паузы опроса при недоступной АИСТ-М (пауза удваивается от PollInterval).
	MaxPollBackoff time.Duration
	// StaleAfter — порог актуальности копии (NFR015): старше — экран предупреждает. Демо-значение.
	StaleAfter time.Duration
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
		c.StatusTimeout = 2 * time.Second
	}
	if c.StatusBudget <= 0 {
		c.StatusBudget = 3 * time.Second
	}
	if c.StatusReserve <= 0 {
		c.StatusReserve = 300 * time.Millisecond
	}
	if c.DBTimeout <= 0 {
		c.DBTimeout = 500 * time.Millisecond
	}
	if c.MaxStatusIDs <= 0 {
		c.MaxStatusIDs = 1000
	}
	if c.MaxPollBackoff <= 0 {
		c.MaxPollBackoff = time.Minute
	}
	if c.StaleAfter <= 0 {
		c.StaleAfter = 10 * time.Minute
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
	metrics metrics
}

// New собирает сервис.
func New(d Deps) *Service {
	return &Service{
		pool: d.Pool, store: d.Store, aist: d.Aist, pub: d.Publisher, journal: d.Journal, ref: d.Ref,
		cat: newCatalog(d.Ref), clock: d.Clock, log: d.Log, cfg: d.Config.withDefaults(), metrics: newMetrics(),
	}
}

// RejectedRecordsMetric — счётчик записей АИСТ-М, не принятых в копию (в Prometheus —
// inventory_aist_rejected_records_total).
const RejectedRecordsMetric = "inventory.aist.rejected_records"

type metrics struct {
	rejected metric.Int64Counter
}

func newMetrics() metrics {
	m := otel.GetMeterProvider().Meter("github.com/AlchemistBro/blood-supply-pet/services/inventory")
	rejected, err := m.Int64Counter(RejectedRecordsMetric,
		metric.WithDescription("Записи АИСТ-М, отброшенные при опросе: неизвестная клиника, вид, группа или статус"))
	if err != nil {
		rejected = noop.Int64Counter{}
	}
	return metrics{rejected: rejected}
}

// MaxStatusIDs — ограничение числа единиц в одном запросе статуса.
func (s *Service) MaxStatusIDs() int { return s.cfg.MaxStatusIDs }
