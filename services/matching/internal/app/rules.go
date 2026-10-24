package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/domain"
)

func (s *Service) SeedRules(ctx context.Context) error {
	return bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		for _, r := range testdata.DemoMatchingRules() {
			rule := domain.Rule{RuleID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(fmt.Sprintf("blood-supply:rule:%s:%d", r.RuleCode, r.VersionNo))), Code: r.RuleCode, Version: r.VersionNo, ComponentKind: r.ComponentKindCode, RequestedGroup: r.RequestedGroupCode, AllowedGroup: r.AllowedGroupCode, AllowedKind: r.AllowedKindCode, Condition: r.ApplicationCondition, IsException: r.IsException, Priority: r.RulePriority, BasisDocument: testdata.DemoRulesBasisDocument, BasisClause: r.BasisClause, ValidFrom: r.ValidFrom, ValidTo: r.ValidTo}
			if err := rule.Validate(); err != nil {
				return err
			}
			if err := s.store.InsertRule(ctx, tx, rule); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *Service) Rules(ctx context.Context) ([]domain.Rule, error) { return s.store.Rules(ctx, nil) }

func (s *Service) AddRule(ctx context.Context, r domain.Rule, actor Actor) error {
	if !slices.Contains(actor.Roles, "IT_SERVICE") {
		return ErrForbidden
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if len(r.Code) > 64 || len(r.Condition) > 512 || len(r.BasisDocument) > 500 || len(r.BasisClause) > 500 {
		return ErrInvalid
	}
	if _, ok := s.ref.Kind(r.ComponentKind); !ok {
		return ErrInvalid
	}
	if r.AllowedKind != "" {
		if _, ok := s.ref.Kind(r.AllowedKind); !ok {
			return ErrInvalid
		}
	}
	if r.RuleID == uuid.Nil {
		var err error
		r.RuleID, err = uuid.NewV7()
		if err != nil {
			return err
		}
	}
	return bspgx.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,1886151026))`, r.Code); err != nil {
			return err
		}
		existing, err := s.store.Rules(ctx, tx)
		if err != nil {
			return err
		}
		var latest *domain.Rule
		for i := range existing {
			old := &existing[i]
			if old.Code == r.Code && (latest == nil || old.Version > latest.Version) {
				latest = old
			}
		}
		if latest == nil {
			if r.Version != 1 {
				return ErrConflict
			}
		} else {
			if r.Version != latest.Version+1 {
				return ErrConflict
			}
			if !r.ValidFrom.After(latest.ValidFrom) || r.ValidFrom.Before(s.clock.Now()) {
				return fmt.Errorf("%w: новая версия не должна менять прошлые решения", ErrInvalid)
			}
			if latest.ValidTo.IsZero() {
				if err := s.store.CloseRule(ctx, tx, *latest, r.ValidFrom); err != nil {
					return err
				}
			} else if r.ValidFrom.Before(latest.ValidTo) {
				return fmt.Errorf("%w: периоды версий пересекаются", ErrInvalid)
			}
		}
		if err := s.store.InsertRule(ctx, tx, r); err != nil {
			return err
		}
		return s.journal.Audit(ctx, tx, messaging.AuditEntry{UserID: actor.User, Role: "IT_SERVICE", Action: "MATCHING_RULE_VERSION_CREATED", ObjectType: "matching_rule", ObjectID: r.RuleID.String(), Result: auditc.ResultSuccess})
	})
}
