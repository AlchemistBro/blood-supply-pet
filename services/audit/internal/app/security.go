package app

import (
	"context"
	"errors"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	securityc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/security"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/services/audit/internal/domain"
)

// SecurityConfig содержит демонстрационные пороги ADR-0025; значения сверяет оператор при внедрении.
type SecurityConfig struct {
	ReadLimit         int           `env:"BS_SECURITY_READ_LIMIT" envDefault:"20"`
	DeniedLimit       int           `env:"BS_SECURITY_DENIED_LIMIT" envDefault:"10"`
	GlobalDeniedLimit int           `env:"BS_SECURITY_GLOBAL_DENIED_LIMIT" envDefault:"30"`
	ExportLimit       int           `env:"BS_SECURITY_EXPORT_LIMIT" envDefault:"3"`
	VerifyInterval    time.Duration `env:"BS_SECURITY_VERIFY_INTERVAL" envDefault:"15m"`
}

func DefaultSecurityConfig() SecurityConfig { return SecurityConfig{20, 10, 30, 3, 15 * time.Minute} }
func (s *Service) ConfigureSecurity() error {
	cfg := DefaultSecurityConfig()
	if err := env.Parse(&cfg); err != nil {
		return err
	}
	if cfg.ReadLimit < 1 || cfg.DeniedLimit < 1 || cfg.GlobalDeniedLimit < 1 || cfg.ExportLimit < 1 || cfg.VerifyInterval < time.Second {
		return errors.New("пороги BS_SECURITY_* должны быть положительными")
	}
	s.security = cfg
	return s.observeSecurity()
}

func (s *Service) detect(ctx context.Context, tx pgx.Tx, r domain.Record) error {
	if r.Action == "READ_PATIENT_CASE" && r.Result == "SUCCESS" && r.UserID != "" {
		if err := s.detectWindow(ctx, tx, r, "PATIENT_CASE_READ_BURST", r.UserID, 600, s.security.ReadLimit+1, "read"); err != nil {
			return err
		}
	}
	if r.Result == "DENIED" || r.Action == "ACCESS_DENIED" {
		if r.UserID != "" {
			if err := s.detectWindow(ctx, tx, r, "ACCESS_DENIED_SERIES", r.UserID, 300, s.security.DeniedLimit, "denied"); err != nil {
				return err
			}
		}
		if err := s.detectWindow(ctx, tx, r, "ACCESS_DENIED_SERIES", "", 300, s.security.GlobalDeniedLimit, "denied"); err != nil {
			return err
		}
	}
	if domain.ForeignClinicRead(r) {
		if err := s.raiseIncident(ctx, tx, "PATIENT_CASE_FOREIGN_CLINIC", r.UserID, 1, 600, r.ReceivedAt, r.Seq); err != nil {
			return err
		}
	}
	if r.Action == "AUDIT_EXPORT" {
		if r.Role != "IT_SERVICE" {
			return s.raiseIncident(ctx, tx, "AUDIT_EXPORT_ATTEMPT", r.UserID, 1, 3600, r.ReceivedAt, r.Seq)
		}
		return s.detectWindow(ctx, tx, r, "AUDIT_EXPORT_ATTEMPT", r.UserID, 3600, s.security.ExportLimit+1, "export")
	}
	return nil
}
func (s *Service) detectWindow(ctx context.Context, tx pgx.Tx, r domain.Record, rule, user string, seconds, threshold int, kind string) error {
	var count int
	var first time.Time
	err := tx.QueryRow(ctx, `SELECT count(*)::int,coalesce(min(received_at),$1) FROM audit_record
 WHERE received_at >= $1 - make_interval(secs => $2) AND received_at <= $1
 AND ($3='' OR user_id=$3) AND
 (($4='read' AND action='READ_PATIENT_CASE' AND result='SUCCESS') OR
 ($4='denied' AND (action='ACCESS_DENIED' OR result='DENIED')) OR
 ($4='export' AND action='AUDIT_EXPORT'))`, r.ReceivedAt, seconds, user, kind).Scan(&count, &first)
	if err != nil {
		return err
	}
	if count < threshold {
		return nil
	}
	return s.raiseIncident(ctx, tx, rule, user, count, seconds, first, r.Seq)
}

// raiseIncident вызывается под блокировкой головы цепочки. Таймер использует ту же блокировку.
func (s *Service) raiseIncident(ctx context.Context, tx pgx.Tx, rule, user string, count, seconds int, first time.Time, seq int64) error {
	now := s.clock.Now()
	var suppressed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM security_incident WHERE rule=$1 AND user_id=$2 AND detected_at > $3 - make_interval(secs => $4))`, rule, user, now, seconds).Scan(&suppressed); err != nil {
		return err
	}
	if suppressed {
		return nil
	}
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO security_incident(incident_id,rule,user_id,count,window_seconds,first_seen_at,detected_at,audit_seq) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, rule, user, count, seconds, first, now, seq); err != nil {
		return err
	}
	_, err = s.journal.Publish(ctx, tx, contracts.TopicSecurityEvents, id.String(), securityc.SecurityIncidentSuspected{IncidentID: id, Rule: rule, UserID: user, Count: count, WindowSeconds: seconds, FirstSeenAt: &first, DetectedAt: now})
	return err
}

// RunVerification проверяет журнал при старте и затем по таймеру; сбой хранилища не считается разрывом.
func (s *Service) RunVerification(ctx context.Context) error {
	ticker := time.NewTicker(s.security.VerifyInterval)
	defer ticker.Stop()
	for {
		if _, err := s.Verify(ctx); err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "плановая проверка аудита не выполнена", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (s *Service) reportBreak(ctx context.Context, broken *domain.BreakReport) error {
	return bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT seq FROM audit_chain_head WHERE id=1 FOR UPDATE`); err != nil {
			return err
		}
		return s.raiseIncident(ctx, tx, "AUDIT_CHAIN_BROKEN", "", 1, 900, s.clock.Now(), broken.Seq)
	})
}
