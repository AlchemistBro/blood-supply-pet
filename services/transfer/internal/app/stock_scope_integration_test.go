//go:build integration

package app_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	auditc "github.com/AlchemistBro/blood-supply-pet/libs/contracts/audit"
	"github.com/AlchemistBro/blood-supply-pet/libs/messaging"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/adapters/postgres"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

type beforeStockLock struct {
	app.Store
	before func()
}

func (s *beforeStockLock) LockStock(ctx context.Context, db app.DB, clinic int64, kind string) (domain.Stock, error) {
	if s.before != nil {
		before := s.before
		s.before = nil
		before()
	}
	return s.Store.LockStock(ctx, db, clinic, kind)
}

func TestWithdrawalSeesAllocationCreatedBeforeStockLock(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 1)
	id := h.open(1, "RBC", 1)
	store := &beforeStockLock{Store: postgres.New(), before: func() {
		if _, ok := h.confirm(id, units, false).(domain.Confirmed); !ok {
			t.Fatal("не создано закрепление между чтением области и блокировкой")
		}
	}}
	svc := app.New(h.pool, store, h.aist, messaging.NewJournal("transfer", auditc.ModuleTransfer, h.clock), h.clock,
		slog.New(slog.NewTextHandler(io.Discard, nil)), app.Config{})
	if err := svc.OnUnitWithdrawn(h.ctx, nil, app.UnitWithdrawn{UnitID: units[0], ClinicID: 1, ComponentKind: "RBC", Reason: "ISSUED", Version: h.versions.Add(1)}); err != nil {
		t.Fatal(err)
	}
	if got := h.view(id).Status; got != domain.StatusBroken {
		t.Fatalf("новое закрепление не компенсировано: %s", got)
	}
	h.assertStockConsistent(1, "RBC")
}
