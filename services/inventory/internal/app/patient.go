package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts"
	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/inventory"
	"github.com/AlchemistBro/blood-supply-pet/libs/k1seal"
	"github.com/AlchemistBro/blood-supply-pet/libs/k1seal/opener"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	bspgx "github.com/AlchemistBro/blood-supply-pet/libs/pgx"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
)

type PatientJob struct {
	ID                int64
	Key, ObligationID uuid.UUID
	Type              string
	Payload           json.RawMessage
	Attempts          int
}
type PatientQueue interface {
	EnqueuePatient(context.Context, pgx.Tx, PatientJob, time.Time) error
	ClaimPatient(context.Context, uuid.UUID, time.Time, time.Time) (PatientJob, bool, error)
	FinishPatient(context.Context, pgx.Tx, PatientJob, uuid.UUID, time.Time, bool) (bool, error)
	PatientCompensationKey(context.Context, PatientJob) (uuid.UUID, bool, error)
	FailPatient(context.Context, PatientJob, uuid.UUID, time.Time) error
	PatientUnitCodes(context.Context, []uuid.UUID) ([]string, error)
	ActivePatientKeys(context.Context, PatientJob) ([]uuid.UUID, error)
	MarkPatientReleased(context.Context, pgx.Tx, PatientJob, time.Time) error
}
type PatientSource interface {
	AllocatePatient(context.Context, uuid.UUID, string, string, []string) (time.Time, error)
	ReleasePatient(context.Context, uuid.UUID, uuid.UUID) error
}
type PatientAllocations struct {
	Pool    *pgxpool.Pool
	Store   PatientQueue
	Source  PatientSource
	Keys    *opener.Keyring
	Journal *messaging.Journal
	Clock   clock.Clock
	Ref     *refdata.Set
	Log     *slog.Logger
}

func (p *PatientAllocations) Handle(ctx context.Context, msg messaging.Message) error {
	job := PatientJob{Type: msg.Type}
	switch msg.Type {
	case "AllocateUnitsToPatient":
		var c inventory.AllocateUnitsToPatient
		if err := msg.Decode(&c); err != nil {
			return messaging.Permanent(err)
		}
		if c.ObligationID == uuid.Nil || c.RequestID == uuid.Nil || c.IdempotencyKey == uuid.Nil || len(c.UnitIDs) == 0 || len(c.UnitIDs) > 10000 {
			return messaging.Permanent(fmt.Errorf("неверное закрепление ИНТ-05"))
		}
		if _, err := k1seal.ParseSealed(c.PatientCaseSealed); err != nil {
			return messaging.Permanent(fmt.Errorf("неверный конверт ИНТ-05"))
		}
		if _, ok := p.Ref.Clinic(c.TargetClinicID); !ok {
			return messaging.Permanent(fmt.Errorf("неизвестная клиника ИНТ-05"))
		}
		seen := map[uuid.UUID]bool{}
		for _, id := range c.UnitIDs {
			if id == uuid.Nil || seen[id] {
				return messaging.Permanent(fmt.Errorf("повтор или пустой идентификатор ИНТ-05"))
			}
			seen[id] = true
		}
		job.Key, job.ObligationID = c.IdempotencyKey, c.ObligationID
		raw, err := json.Marshal(c)
		if err != nil {
			return err
		}
		job.Payload = raw
	case "ReleasePatientAllocation":
		var c inventory.ReleasePatientAllocation
		if err := msg.Decode(&c); err != nil {
			return messaging.Permanent(err)
		}
		if c.ObligationID == uuid.Nil || c.IdempotencyKey == uuid.Nil || (c.Reason != "" && !slices.Contains(inventory.ReleaseReasons, c.Reason)) {
			return messaging.Permanent(fmt.Errorf("неверное снятие ИНТ-05"))
		}
		job.Key, job.ObligationID = c.IdempotencyKey, c.ObligationID
		raw, err := json.Marshal(c)
		if err != nil {
			return err
		}
		job.Payload = raw
	default:
		return nil
	}
	return bspgx.WithTx(ctx, p.Pool, func(ctx context.Context, tx pgx.Tx) error {
		first, err := messaging.FirstTime(ctx, tx, msg, p.Clock.Now())
		if err != nil || !first {
			return err
		}
		return p.Store.EnqueuePatient(ctx, tx, job, p.Clock.Now())
	})
}

// DeliverOne берёт только первую неподтверждённую запись каждого O07; вызов C01 вне транзакции.
func (p *PatientAllocations) DeliverOne(ctx context.Context) (bool, error) {
	owner := uuid.New()
	job, ok, err := p.Store.ClaimPatient(ctx, owner, p.Clock.Now(), p.Clock.Now().Add(45*time.Second))
	if err != nil || !ok {
		return false, err
	}
	ackAt := p.Clock.Now()
	compensated := false
	compensationKey, hasCompensation, checkErr := p.Store.PatientCompensationKey(ctx, job)
	switch {
	case checkErr != nil:
		err = checkErr
	case job.Type == "AllocateUnitsToPatient" && hasCompensation:
		compensated = true
		err = p.Source.ReleasePatient(ctx, job.Key, compensationKey)
	case job.Type == "AllocateUnitsToPatient":
		var c inventory.AllocateUnitsToPatient
		if err = json.Unmarshal(job.Payload, &c); err == nil {
			var codes []string
			codes, err = p.Store.PatientUnitCodes(ctx, c.UnitIDs)
			if err == nil {
				var plain string
				plain, err = p.Keys.Open(k1seal.Sealed(c.PatientCaseSealed), c.RequestID.String())
				if err == nil {
					clinic, _ := p.Ref.Clinic(c.TargetClinicID)
					ackAt, err = p.Source.AllocatePatient(ctx, job.Key, plain, clinic.ClinicCode, codes)
				}
			}
		}
	default:
		var keys []uuid.UUID
		keys, err = p.Store.ActivePatientKeys(ctx, job)
		if err == nil {
			for _, key := range keys {
				if err = p.Source.ReleasePatient(ctx, key, job.Key); err != nil {
					break
				}
			}
		}
	}
	if err != nil {
		wait := time.Second * time.Duration(1<<min(job.Attempts, 6))
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if saveErr := p.Store.FailPatient(saveCtx, job, owner, p.Clock.Now().Add(wait)); saveErr != nil {
			return true, saveErr
		}
		// Только идентификатор и попытка: сообщения C01 и конверт в журнал не попадают.
		p.Log.WarnContext(ctx, "ИНТ-05 ожидает повторной доставки", "obligation_id", job.ObligationID, "attempt", job.Attempts)
		return true, nil
	}
	err = bspgx.WithTx(ctx, p.Pool, func(ctx context.Context, tx pgx.Tx) error {
		finished, err := p.Store.FinishPatient(ctx, tx, job, owner, ackAt, compensated)
		if err != nil || !finished {
			return err
		}
		if job.Type == "AllocateUnitsToPatient" && !compensated {
			if _, err := p.Journal.Publish(ctx, tx, contracts.TopicInventoryEvents, job.ObligationID.String(), inventory.PatientAllocationAcknowledged{ObligationID: job.ObligationID, IdempotencyKey: job.Key, AcknowledgedAt: ackAt}); err != nil {
				return err
			}
		} else {
			if err := p.Store.MarkPatientReleased(ctx, tx, job, p.Clock.Now()); err != nil {
				return err
			}
		}
		return p.Journal.Audit(ctx, tx, messaging.AuditEntry{UserID: "inventory", Action: "PATIENT_ALLOCATION_DELIVERED", ObjectType: "transfer_obligation", ObjectID: job.ObligationID.String(), Result: auditc.ResultSuccess, Details: map[string]any{"command": job.Type, "idempotency_key": job.Key.String(), "compensated": compensated}})
	})
	return true, err
}

func (p *PatientAllocations) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for range 100 {
			worked, err := p.DeliverOne(ctx)
			if err != nil && ctx.Err() == nil {
				p.Log.ErrorContext(ctx, "очередь ИНТ-05 недоступна", "err", err)
			}
			if !worked || err != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
