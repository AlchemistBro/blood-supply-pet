//go:build integration

package app_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/transfer/internal/domain"
)

// Сценарии саги подтверждения без гонок: перенос TransferScenarioTests.java и новые сценарии этапа 1.

func TestRepeatedChoiceDoesNotOpenSecondObligation(t *testing.T) {
	h := setup(t)
	cmd := newObligation(1, "RBC", 1, nil, domain.ReplacementSpec{RuleVersion: rule})

	first := h.openWith(cmd)
	if second := h.openWith(cmd); second != first {
		t.Fatalf("повтор выбора открыл другое задание: %s и %s", first, second)
	}
	list, err := h.svc.ByRequest(h.ctx, cmd.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("по заявке %d заданий, ожидали 1", len(list))
	}
	h.expectTypes("события", h.eventTypes(first), "ObligationOpened")
}

func TestRepeatedConfirmationWithSameKeyIsIdempotent(t *testing.T) {
	h := setup(t)
	unit := h.registerUnits(1, "RBC", 1)[0]
	o := h.open(1, "RBC", 1)
	cmd := app.ConfirmTransfer{ObligationID: o, UnitIDs: []uuid.UUID{unit}, IdempotencyKey: "key-1", Operator: "operator-1"}

	first, err := h.svc.Confirm(h.ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := first.(domain.Confirmed); !ok {
		t.Fatalf("первое подтверждение: %#v", first)
	}
	second, err := h.svc.Confirm(h.ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("повтор с тем же ключом: %#v, первый ответ %#v", second, first)
	}
	h.expectTypes("события", h.eventTypes(o), "ObligationOpened", "TransferConfirmed")
	h.expectTypes("команды", h.commandTypes(o), "RequestTransport")
	h.assertStockConsistent(1, "RBC")
}

func TestAllUnitsWithdrawnRejectsObligation(t *testing.T) {
	h := setup(t)
	unit := h.registerUnits(1, "FFP", 1)[0]
	o := h.open(1, "FFP", 1)
	h.aist.set(unit, domain.UnitIssued) // АИСТ-М уже знает о выдаче, событие ещё не дошло

	if res := h.confirm(o, []uuid.UUID{unit}, true); !isType[domain.Rejected](res) {
		t.Fatalf("ожидали Rejected, получили %#v", res)
	}
	if s := h.view(o).Status; s != domain.StatusRejected {
		t.Fatalf("статус %s, ожидали REJECTED", s)
	}
	h.expectTypes("события", h.eventTypes(o), "ObligationOpened", "TransferRejected")
	h.expectTypes("команды", h.commandTypes(o))
}

func TestAistMDownLeavesObligationPending(t *testing.T) {
	h := setup(t)
	unit := h.registerUnits(1, "RBC", 1)[0]
	o := h.open(1, "RBC", 1)

	h.aist.setDown(true)
	if res := h.confirm(o, []uuid.UUID{unit}, false); !isType[domain.SourceSystemUnavailable](res) {
		t.Fatalf("АИСТ-М не отвечает: ожидали SourceSystemUnavailable, получили %#v", res)
	}
	h.aist.setDown(false)

	// Ответ inventory из копии (source: copy) — тоже не основание закреплять (ADR-0004, решение этапа 1).
	h.aist.setFromCopy(true)
	if res := h.confirm(o, []uuid.UUID{unit}, false); !isType[domain.SourceSystemUnavailable](res) {
		t.Fatalf("ответ из копии: ожидали SourceSystemUnavailable, получили %#v", res)
	}
	h.aist.setFromCopy(false)

	if s := h.view(o).Status; s != domain.StatusPending {
		t.Fatalf("статус %s, ожидали PENDING", s)
	}
	if n := h.count(`SELECT count(*) FROM allocation`); n != 0 {
		t.Fatalf("вслепую закреплено %d единиц", n)
	}
	h.expectTypes("события планового задания", h.eventTypes(o), "ObligationOpened")
}

// Экстренное задание при недоступной АИСТ-М: отказ «повторите» и эскалация дежурному, статус не меняется.
func TestEmergencyWithAistMDownRaisesEscalation(t *testing.T) {
	h := setup(t)
	unit := h.registerUnits(1, "RBC", 1)[0]
	cmd := newObligation(1, "RBC", 1, nil, domain.ReplacementSpec{RuleVersion: rule})
	cmd.UrgencyCode = domain.UrgencyEmergency
	o := h.openWith(cmd)

	h.aist.setDown(true)
	for i := 0; i < 3; i++ { // оператор повторяет: эскалация одна
		if res := h.confirm(o, []uuid.UUID{unit}, false); !isType[domain.SourceSystemUnavailable](res) {
			t.Fatalf("ожидали SourceSystemUnavailable, получили %#v", res)
		}
	}
	if s := h.view(o).Status; s != domain.StatusPending {
		t.Fatalf("статус %s, ожидали PENDING", s)
	}
	h.expectTypes("события", h.eventTypes(o), "ObligationOpened", "EscalationRaised")
	h.expectTypes("аудит", h.auditActions(o), app.ActionOpened, app.ActionEscalated)
}

func TestTimerEscalatesToNextSourceAndExpiresWithoutOne(t *testing.T) {
	h := setup(t)
	withFallback := h.openWith(newObligation(1, "PLT", 1, []int64{2}, domain.ReplacementSpec{RuleVersion: rule}))
	withoutFallback := h.open(1, "PLT", 1)
	h.clock.Advance(16 * time.Minute)

	escalations, err := h.svc.ExpireOverdue(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var next *uuid.UUID
	sawWithout := false
	for _, e := range escalations {
		if e.ExpiredID == withFallback {
			next = e.NextID
		}
		if e.ExpiredID == withoutFallback && e.NextID == nil {
			sawWithout = true
		}
	}
	if next == nil {
		t.Fatalf("эскалация к следующему источнику не открыла задание: %+v", escalations)
	}
	nv := h.view(*next)
	if nv.SourceClinicID != 2 || nv.Status != domain.StatusPending {
		t.Fatalf("новое задание: источник %d, статус %s", nv.SourceClinicID, nv.Status)
	}
	if nv.ChoiceID != domain.EscalationChoiceID(withFallback) {
		t.Errorf("choice_id нового задания %s, ожидали uuid5 от старого id", nv.ChoiceID)
	}
	if !sawWithout {
		t.Fatalf("нет эскалации без следующего источника: %+v", escalations)
	}
	if s := h.view(withoutFallback).Status; s != domain.StatusExpired {
		t.Fatalf("без запасного источника статус %s, ожидали EXPIRED", s)
	}
	if s := h.view(withFallback).Status; s != domain.StatusExpired {
		t.Fatalf("с запасным источником статус %s, ожидали EXPIRED", s)
	}
	h.expectTypes("события без запасного", h.eventTypes(withoutFallback), "ObligationOpened", "TransferExpired", "EscalationRaised")
	h.expectTypes("события с запасным", h.eventTypes(withFallback), "ObligationOpened", "TransferExpired")

	// Повтор таймера ничего не открывает второй раз.
	again, err := h.svc.ExpireOverdue(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("повтор таймера: %+v", again)
	}
	list, err := h.svc.ByRequest(h.ctx, h.view(withFallback).RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("по заявке %d заданий, ожидали 2 (истёкшее и новое)", len(list))
	}
}

// Одно битое задание (неизвестный запасной источник: FK при открытии эскалации) не мешает истечь остальным.
func TestBrokenObligationDoesNotBlockExpiryOfOthers(t *testing.T) {
	h := setup(t)
	broken := h.openWith(newObligation(1, "PLT", 1, []int64{2}, domain.ReplacementSpec{RuleVersion: rule}))
	healthy := h.open(1, "PLT", 1)
	// Обход проверки открытия: так выглядело бы задание, записанное до неё (у массива нет FK).
	if _, err := h.pool.Exec(h.ctx, `UPDATE obligation SET fallback_sources = '{999999}' WHERE obligation_id = $1`, broken); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(16 * time.Minute)

	escalations, err := h.svc.ExpireOverdue(h.ctx)
	if err == nil {
		t.Error("ошибка битого задания должна вернуться вызывающему")
	}
	if s := h.view(healthy).Status; s != domain.StatusExpired {
		t.Fatalf("исправное задание не истекло из-за битого: %s", s)
	}
	if s := h.view(broken).Status; s != domain.StatusPending {
		t.Fatalf("битое задание: %s, ожидали откат и PENDING", s)
	}
	if len(escalations) != 1 || escalations[0].ExpiredID != healthy {
		t.Fatalf("эскалации: %+v", escalations)
	}
}

// Неизвестный запасной источник отклоняется при открытии, а не при эскалации.
func TestOpenRejectsUnknownFallbackClinic(t *testing.T) {
	h := setup(t)
	_, err := h.svc.Open(h.ctx, nil, newObligation(1, "RBC", 1, []int64{999999}, domain.ReplacementSpec{RuleVersion: rule}))
	if !errors.Is(err, domain.ErrInvalidObligation) {
		t.Fatalf("ожидали ErrInvalidObligation, получили %v", err)
	}
}

// Гонка 3 в одиночку: позднее подтверждение истёкшего задания — «не отправляйте».
func TestLateConfirmationAfterExpiryIsWithdrawn(t *testing.T) {
	h := setup(t)
	unit := h.registerUnits(1, "RBC", 1)[0]
	o := h.openWith(newObligation(1, "RBC", 1, []int64{2}, domain.ReplacementSpec{RuleVersion: rule}))
	h.clock.Advance(16 * time.Minute)
	if _, err := h.svc.ExpireOverdue(h.ctx); err != nil {
		t.Fatal(err)
	}
	res := h.confirm(o, []uuid.UUID{unit}, false)
	if w, ok := res.(domain.TaskWithdrawn); !ok || w.Status != domain.StatusExpired {
		t.Fatalf("ожидали TaskWithdrawn(EXPIRED), получили %#v", res)
	}
	if n := h.count(`SELECT count(*) FROM allocation`); n != 0 {
		t.Fatalf("закреплено %d единиц", n)
	}
}

func TestReplacementFollowsSpecFromDoctorsChoice(t *testing.T) {
	h := setup(t)
	original := h.registerUnit(1, "RBC", "A", true, h.inDays(10))
	wrongGroup := h.registerUnit(1, "RBC", "B", true, h.inDays(10))
	expiresTooSoon := h.registerUnit(1, "RBC", "A", true, h.inDays(1))
	equivalent := h.registerUnit(1, "RBC", "A", true, h.inDays(12))
	spec := domain.ReplacementSpec{BloodGroup: ptr("A"), RhPositive: ptr(true), MinExpiresAt: ptr(h.inDays(3)), RuleVersion: rule}
	o := h.openWith(newObligation(1, "RBC", 1, nil, spec))
	if res := h.confirm(o, []uuid.UUID{original}, false); !isType[domain.Confirmed](res) {
		t.Fatalf("подтверждение: %#v", res)
	}

	if res := h.replace(o, original, wrongGroup); !isType[domain.SpecMismatch](res) {
		t.Errorf("другая группа: %#v", res)
	}
	if res := h.replace(o, original, expiresTooSoon); !isType[domain.SpecMismatch](res) {
		t.Errorf("годность до прибытия: %#v", res)
	}
	want := domain.Replaced{ObligationID: o, OldUnitID: original, NewUnitID: equivalent}
	if res := h.replace(o, original, equivalent); !reflect.DeepEqual(res, want) {
		t.Fatalf("равноценная замена: %#v", res)
	}

	v := h.view(o)
	if !equal(v.Units, []uuid.UUID{equivalent}) || v.Status != domain.StatusConfirmed {
		t.Fatalf("после замены: единицы %v, статус %s", v.Units, v.Status)
	}
	h.expectTypes("события", h.eventTypes(o), "ObligationOpened", "TransferConfirmed", "TransferUnitReplaced")
	h.expectTypes("команды (замена заказ не трогает)", h.commandTypes(o), "RequestTransport")
	h.assertStockConsistent(1, "RBC")
}

func TestReplacementIsNotAllowedBeforeConfirmation(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 2)
	o := h.open(1, "RBC", 1)

	if res := h.replace(o, units[0], units[1]); !reflect.DeepEqual(res, domain.NotAllowed{Status: domain.StatusPending}) {
		t.Fatalf("ожидали NotAllowed(PENDING), получили %#v", res)
	}
}

func TestWithdrawalOfAllocatedUnitBreaksObligationAndReleasesUnits(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 2)
	o := h.open(1, "RBC", 2)
	if res := h.confirm(o, units, false); !isType[domain.Confirmed](res) {
		t.Fatalf("подтверждение: %#v", res)
	}

	h.withdraw(units[0], 1, "RBC")

	v := h.view(o)
	if v.Status != domain.StatusBroken || len(v.Units) != 0 {
		t.Fatalf("после выбытия: статус %s, единицы %v", v.Status, v.Units)
	}
	if v.StatusReason != string(domain.BreakUnitWithdrawn) {
		t.Errorf("причина %q", v.StatusReason)
	}
	h.expectTypes("события", h.eventTypes(o), "ObligationOpened", "TransferConfirmed", "TransferBroken")
	h.expectTypes("команды", h.commandTypes(o), "RequestTransport", "CancelTransport")
	if r := h.payloadField(o, "TransferBroken", "reason"); r != "ISSUED" {
		t.Errorf("TransferBroken.reason = %q, ожидали причину выбытия ISSUED", r)
	}
	if r := h.payloadField(o, "CancelTransport", "reason"); r != "UNIT_WITHDRAWN" {
		t.Errorf("CancelTransport.reason = %q", r)
	}
	h.assertStockConsistent(1, "RBC")
}

// Kafka доставляет «хотя бы один раз»: повтор того же события выбытия ничего не меняет.
func TestDuplicateWithdrawalDeliveryChangesNothing(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 1)
	o := h.open(1, "RBC", 1)
	h.confirm(o, units, false)
	msg := delivery()
	ev := app.UnitWithdrawn{UnitID: units[0], ClinicID: 1, ComponentKind: "RBC", Reason: "ISSUED", Version: h.versions.Add(1)}
	for i := 0; i < 3; i++ {
		if err := h.svc.OnUnitWithdrawn(h.ctx, msg, ev); err != nil {
			t.Fatal(err)
		}
	}
	h.expectTypes("события", h.eventTypes(o), "ObligationOpened", "TransferConfirmed", "TransferBroken")
	h.assertStockConsistent(1, "RBC")
}

// Единица на карантине не закрепляется никогда (ПП РФ № 641, п. 62 а): ни по ответу АИСТ-М, ни по копии, ни заменой.
func TestQuarantinedUnitIsNeverAllocated(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 3)
	h.quarantine(units[0], 1, "RBC")
	o := h.open(1, "RBC", 2)

	res := h.confirm(o, []uuid.UUID{units[0], units[1]}, false)
	if u, ok := res.(domain.UnitsUnavailable); !ok || !equal(u.Units, []uuid.UUID{units[0]}) {
		t.Fatalf("без частичного: ожидали UnitsUnavailable([карантинная]), получили %#v", res)
	}
	if s := h.view(o).Status; s != domain.StatusPending {
		t.Fatalf("статус %s, ожидали PENDING", s)
	}

	// АИСТ-М уже сняла карантин в ответе, а копия ещё нет: перепроверка под блокировкой не пускает.
	h.aist.set(units[0], domain.UnitInStock)
	if res := h.confirm(o, []uuid.UUID{units[0]}, false); !isType[domain.UnitsUnavailable](res) {
		t.Fatalf("копия на карантине: ожидали UnitsUnavailable, получили %#v", res)
	}
	h.aist.set(units[0], domain.UnitQuarantine)

	res = h.confirm(o, []uuid.UUID{units[0], units[1]}, true)
	c, ok := res.(domain.Confirmed)
	if !ok || !equal(c.Units, []uuid.UUID{units[1]}) || c.Shortfall != 1 {
		t.Fatalf("частичное: ожидали Confirmed([вторая], недостача 1), получили %#v", res)
	}
	if rr := h.replace(o, units[1], units[0]); !isType[domain.UnitUnavailable](rr) {
		t.Fatalf("замена на карантинную: ожидали UnitUnavailable, получили %#v", rr)
	}
	if n := h.count(`SELECT count(*) FROM allocation WHERE unit_id = $1`, units[0]); n != 0 {
		t.Fatalf("карантинная единица закреплялась %d раз", n)
	}
	h.expectTypes("события", h.eventTypes(o), "ObligationOpened", "TransferConfirmed", "TransferShortfallRecorded")
	h.assertStockConsistent(1, "RBC")
}

func TestQuarantineOfAllocatedUnitBreaksObligation(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 2)
	o := h.open(1, "RBC", 2)
	h.confirm(o, units, false)

	h.quarantine(units[1], 1, "RBC")

	v := h.view(o)
	if v.Status != domain.StatusBroken || len(v.Units) != 0 {
		t.Fatalf("после карантина: статус %s, единицы %v", v.Status, v.Units)
	}
	if v.StatusReason != string(domain.BreakUnitQuarantined) {
		t.Errorf("причина %q, ожидали UNIT_QUARANTINED", v.StatusReason)
	}
	if r := h.payloadField(o, "TransferBroken", "reason"); r != "QUARANTINE" {
		t.Errorf("TransferBroken.reason = %q, ожидали QUARANTINE", r)
	}
	if r := h.payloadField(o, "CancelTransport", "reason"); r != "UNIT_QUARANTINED" {
		t.Errorf("CancelTransport.reason = %q, ожидали UNIT_QUARANTINED", r)
	}
	h.expectTypes("события", h.eventTypes(o), "ObligationOpened", "TransferConfirmed", "TransferBroken")
	h.expectTypes("команды", h.commandTypes(o), "RequestTransport", "CancelTransport")
	h.assertStockConsistent(1, "RBC")
	if n := h.count(`SELECT available_units FROM source_stock WHERE clinic_id = 1 AND component_kind_code = 'RBC'`); n != 1 {
		t.Errorf("в наличии %d, карантинная не должна считаться", n)
	}
}

// Единица может прийти сразу на карантине: в наличие не входит и не закрепляется.
func TestUnitRegisteredInQuarantineIsNotAvailable(t *testing.T) {
	h := setup(t)
	id := uuid.New()
	h.aist.set(id, domain.UnitQuarantine)
	if err := h.svc.OnUnitRegistered(h.ctx, delivery(), app.UnitRegistered{
		UnitID: id, ClinicID: 1, ComponentKind: "RBC", BloodGroup: "A", RhPositive: true, VolumeML: 300,
		ExpiresAt: h.inDays(20), Status: domain.UnitQuarantine, Version: h.versions.Add(1),
	}); err != nil {
		t.Fatal(err)
	}
	if n := h.count(`SELECT available_units FROM source_stock WHERE clinic_id = 1 AND component_kind_code = 'RBC'`); n != 0 {
		t.Fatalf("в наличии %d, карантинная не должна считаться", n)
	}
	o := h.open(1, "RBC", 1)
	if res := h.confirm(o, []uuid.UUID{id}, true); !isType[domain.Rejected](res) {
		t.Fatalf("ожидали Rejected, получили %#v", res)
	}
	if n := h.count(`SELECT count(*) FROM allocation`); n != 0 {
		t.Fatalf("закреплено %d", n)
	}
}

func TestCancelReleasesUnitsAndReserve(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 2)
	o := h.open(1, "RBC", 2)
	h.confirm(o, units, false)

	res := h.cancel(o)
	if !res.Cancelled || res.Status != domain.StatusCancelled {
		t.Fatalf("отмена: %+v", res)
	}
	v := h.view(o)
	if v.Status != domain.StatusCancelled || len(v.Units) != 0 {
		t.Fatalf("после отмены: статус %s, единицы %v", v.Status, v.Units)
	}
	h.expectTypes("события", h.eventTypes(o), "ObligationOpened", "TransferConfirmed", "TransferCancelled")
	// RequestTransport уже ушла при подтверждении: отмена снимает заказ, даже если ответа шлюза ещё не было.
	h.expectTypes("команды", h.commandTypes(o), "RequestTransport", "CancelTransport")
	h.assertStockConsistent(1, "RBC")

	// Отмена ещё не подтверждённого задания тоже работает.
	pending := h.open(1, "RBC", 1)
	if r := h.cancel(pending); !r.Cancelled {
		t.Fatalf("отмена PENDING: %+v", r)
	}
	h.expectTypes("события PENDING", h.eventTypes(pending), "ObligationOpened", "TransferCancelled")
}

func TestCancelAfterTransportRequestedSendsCancelTransport(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 1)
	o := h.open(1, "RBC", 1)
	h.confirm(o, units, false)
	h.reply(app.LogisticsReply{Kind: app.ReplyAccepted, ObligationID: o, ShipmentRef: "c03-1"})
	if s := h.view(o).Status; s != domain.StatusTransportRequested {
		t.Fatalf("после TransportAccepted статус %s", s)
	}

	if res := h.cancel(o); !res.Cancelled {
		t.Fatalf("отмена: %+v", res)
	}
	h.expectTypes("команды", h.commandTypes(o), "RequestTransport", "CancelTransport")
	h.expectTypes("события", h.eventTypes(o), "ObligationOpened", "TransferConfirmed", "TransportRequested", "TransferCancelled")
	if r := h.payloadField(o, "CancelTransport", "reason"); r != "OBLIGATION_CANCELLED" {
		t.Errorf("CancelTransport.reason = %q", r)
	}
	h.assertStockConsistent(1, "RBC")
}

func TestCancelOfFinishedObligationChangesNothing(t *testing.T) {
	h := setup(t)
	unit := h.registerUnits(1, "RBC", 1)[0]
	o := h.open(1, "RBC", 1)
	h.aist.set(unit, domain.UnitIssued)
	h.confirm(o, []uuid.UUID{unit}, false) // все выбыли → REJECTED
	before := h.view(o)
	outbox := h.outboxCount()
	audit := len(h.auditActions(o))

	res := h.cancel(o)
	if res.Cancelled || res.Status != domain.StatusRejected {
		t.Fatalf("отмена конечного: %+v", res)
	}
	after := h.view(o)
	if after.Status != before.Status || after.Version != before.Version {
		t.Fatalf("конечное задание изменилось: %s v%d → %s v%d", before.Status, before.Version, after.Status, after.Version)
	}
	if h.outboxCount() != outbox || len(h.auditActions(o)) != audit {
		t.Fatal("отмена конечного задания записала события или аудит")
	}
}

// Строки закреплений не удаляются: по ним видно, куда уходила единица (прослеживаемость, ПП РФ № 641, п. 17–18, 54).
func TestAllocationHistoryIsKeptAfterRelease(t *testing.T) {
	h := setup(t)
	unit := h.registerUnits(1, "RBC", 1)[0]
	first := h.open(1, "RBC", 1)
	h.confirm(first, []uuid.UUID{unit}, false)
	h.cancel(first)
	second := h.open(1, "RBC", 1)
	if res := h.confirm(second, []uuid.UUID{unit}, false); !isType[domain.Confirmed](res) {
		t.Fatalf("повторное закрепление снятой единицы: %#v", res)
	}

	history, err := h.svc.AllocationHistory(h.ctx, unit)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("история закреплений: %d строк, ожидали 2", len(history))
	}
	if history[0].ObligationID != first || history[0].Status != domain.AllocationReleased || history[0].ClosedAt == nil {
		t.Errorf("первое закрепление: %+v", history[0])
	}
	if history[1].ObligationID != second || history[1].Status != domain.AllocationConfirmed || history[1].ClosedAt != nil {
		t.Errorf("второе закрепление: %+v", history[1])
	}
	if _, err := h.pool.Exec(h.ctx, `DELETE FROM allocation WHERE unit_id = $1`, unit); err == nil {
		t.Fatal("DELETE закреплений должен быть запрещён триггером")
	}
}

// Защищённый объём (NFR013) без гонки: подтверждение ниже защищённого объёма не проходит.
func TestProtectedVolumeIsRespected(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 4)
	h.protect(1, "RBC", 3)
	o := h.open(1, "RBC", 2)

	res := h.confirm(o, units[:2], false)
	if p, ok := res.(domain.ProtectedVolumeExceeded); !ok || p.MaxConfirmable != 1 {
		t.Fatalf("ожидали ProtectedVolumeExceeded(1), получили %#v", res)
	}
	if res := h.confirm(o, units[:1], false); !isType[domain.Confirmed](res) {
		t.Fatalf("одну можно: %#v", res)
	}
	h.assertStockConsistent(1, "RBC")
}

// Ответы шлюза логистики (решения этапа 1): дубль, поздний ответ, отказ, неизвестный исход.
func TestLogisticsRepliesFollowSaga(t *testing.T) {
	h := setup(t)
	units := h.registerUnits(1, "RBC", 3)

	accepted := h.open(1, "RBC", 1)
	h.confirm(accepted, units[:1], false)
	msg := delivery()
	r := app.LogisticsReply{Kind: app.ReplyAccepted, ObligationID: accepted, ShipmentRef: "c03-1"}
	for i := 0; i < 2; i++ {
		if err := h.svc.OnLogisticsReply(h.ctx, msg, r); err != nil {
			t.Fatal(err)
		}
	}
	h.reply(r) // тот же ответ другим сообщением
	if v := h.view(accepted); v.Status != domain.StatusTransportRequested || v.TransportRef != "c03-1" {
		t.Fatalf("принятый заказ: %s %q", v.Status, v.TransportRef)
	}
	h.expectTypes("события принятого", h.eventTypes(accepted), "ObligationOpened", "TransferConfirmed", "TransportRequested")

	unknown := h.open(1, "RBC", 1)
	h.confirm(unknown, units[1:2], false)
	h.reply(app.LogisticsReply{Kind: app.ReplyRejected, ObligationID: unknown, Reason: app.RejectOutcomeUnknown, Details: "C03 не отвечает"})
	if s := h.view(unknown).Status; s != domain.StatusConfirmed {
		t.Fatalf("неизвестный исход: статус %s, ожидали CONFIRMED", s)
	}
	h.expectTypes("события неизвестного исхода", h.eventTypes(unknown), "ObligationOpened", "TransferConfirmed", "EscalationRaised")
	h.reply(app.LogisticsReply{Kind: app.ReplyAccepted, ObligationID: unknown, ShipmentRef: "c03-2"})
	if s := h.view(unknown).Status; s != domain.StatusTransportRequested {
		t.Fatalf("запоздалый TransportAccepted после неизвестного исхода: статус %s", s)
	}

	refused := h.open(1, "RBC", 1)
	h.confirm(refused, units[2:], false)
	h.reply(app.LogisticsReply{Kind: app.ReplyRejected, ObligationID: refused, Reason: app.RejectRefused, Details: "нет машины"})
	if s := h.view(refused).Status; s != domain.StatusConfirmed {
		t.Fatalf("отказ перевозчика: статус %s, ожидали CONFIRMED", s)
	}
	h.expectTypes("события отказа", h.eventTypes(refused), "ObligationOpened", "TransferConfirmed", "TransportRequestFailed", "EscalationRaised")

	h.withdraw(units[2], 1, "RBC")
	h.reply(app.LogisticsReply{Kind: app.ReplyAccepted, ObligationID: refused, ShipmentRef: "c03-late"})
	if s := h.view(refused).Status; s != domain.StatusBroken {
		t.Fatalf("поздний TransportAccepted оживил нарушенное задание: %s", s)
	}
	actions := h.auditActions(refused)
	if actions[len(actions)-1] != app.ActionTransportReplyIgnored {
		t.Errorf("последнее действие аудита %s, ожидали %s", actions[len(actions)-1], app.ActionTransportReplyIgnored)
	}
}

func isType[T any](v any) bool {
	_, ok := v.(T)
	return ok
}
