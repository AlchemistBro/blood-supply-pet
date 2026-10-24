package domain_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/services/notification/internal/domain"
)

var (
	obligationID = uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-300000000001")
	requestID    = uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-200000000001")
	deadline     = time.Date(2026, 10, 23, 10, 15, 0, 0, time.UTC)
)

func opened(origin, urgency string) domain.Obligation {
	return domain.Obligation{
		ObligationID: obligationID, RequestID: requestID, Origin: origin,
		SourceClinicID: 1, TargetClinicID: 2, ComponentKind: "RBC", RequestedQty: 3,
		Urgency: urgency, Deadline: deadline,
	}
}

// FR044: задание по выбору врача — письмо оператору клиники-источника со сведениями о задании.
func TestDoctorChoiceNotifiesSourceOperator(t *testing.T) {
	for _, urgency := range []string{"", domain.UrgencyUrgent, domain.UrgencyEmergency} {
		d, ok := domain.OnObligationOpened(opened(domain.OriginDoctorChoice, urgency))
		if !ok {
			t.Fatalf("срочность %q: письма нет, а задание создано выбором врача", urgency)
		}
		if d.Kind != domain.KindUrgentTask {
			t.Fatalf("вид %s, ожидался %s", d.Kind, domain.KindUrgentTask)
		}
		if d.To.Role != domain.RoleOperator || d.To.ClinicID == nil || *d.To.ClinicID != 1 {
			t.Fatalf("адресат %+v, ожидался оператор клиники-источника 1", d.To)
		}
		f := d.Facts
		if f.ObligationID != obligationID || f.RequestID != requestID {
			t.Fatalf("идентификаторы задания не перенесены: %+v", f)
		}
		if f.SourceClinicID == nil || *f.SourceClinicID != 1 || f.TargetClinicID == nil || *f.TargetClinicID != 2 {
			t.Fatalf("клиники не перенесены: %+v", f)
		}
		if f.ComponentKind != "RBC" || f.Quantity == nil || *f.Quantity != 3 {
			t.Fatalf("вид и количество не перенесены: %+v", f)
		}
		if f.Deadline == nil || !f.Deadline.Equal(deadline) || f.Urgency != urgency {
			t.Fatalf("срок и срочность не перенесены: %+v", f)
		}
	}
}

// Плановое задание срочным не считается, задание из плана пополнения письма не порождает (FR044 — только выбор врача).
func TestNoLetterForPlannedOrPlanProposal(t *testing.T) {
	cases := map[string]domain.Obligation{
		"плановая срочность":        opened(domain.OriginDoctorChoice, domain.UrgencyPlanned),
		"предложение плана":         opened(domain.OriginPlanProposal, ""),
		"предложение плана, EMRG":   opened(domain.OriginPlanProposal, domain.UrgencyEmergency),
		"неизвестное происхождение": opened("SOMETHING_NEW", domain.UrgencyUrgent),
	}
	for name, o := range cases {
		if d, ok := domain.OnObligationOpened(o); ok {
			t.Errorf("%s: письмо не ожидалось, получено %+v", name, d)
		}
	}
}

// FR044: при выборе местного варианта письмо не отправляется.
func TestNoLetterForLocalVariant(t *testing.T) {
	o := opened(domain.OriginDoctorChoice, domain.UrgencyUrgent)
	o.TargetClinicID = o.SourceClinicID
	if _, ok := domain.OnObligationOpened(o); ok {
		t.Fatal("источник совпадает с получателем — письмо не ожидалось")
	}
}

// Эскалация уходит дежурному смены; сведения о задании берутся из проекции, если она есть.
func TestEscalationGoesToDutyOfficer(t *testing.T) {
	o := opened(domain.OriginDoctorChoice, "")
	d := domain.OnEscalation(obligationID, requestID, "EXPIRED_NO_FALLBACK", &o)
	if d.Kind != domain.KindEscalation || d.To.Role != domain.RoleDuty || d.To.ClinicID != nil {
		t.Fatalf("черновик %+v: ожидалась эскалация дежурному без клиники", d)
	}
	if d.Facts.Reason != "EXPIRED_NO_FALLBACK" || d.Facts.SourceClinicID == nil || d.Facts.ComponentKind != "RBC" {
		t.Fatalf("сведения не перенесены: %+v", d.Facts)
	}

	bare := domain.OnEscalation(obligationID, requestID, "TRANSPORT_REFUSED", nil)
	if bare.Facts.ObligationID != obligationID || bare.Facts.Reason != "TRANSPORT_REFUSED" {
		t.Fatalf("без проекции письмо всё равно содержит номер задания и причину: %+v", bare.Facts)
	}
	if bare.Facts.SourceClinicID != nil || bare.Facts.Quantity != nil || bare.Facts.ComponentKind != "" {
		t.Fatalf("без проекции сведений о задании быть не должно: %+v", bare.Facts)
	}
}

// Нарушение обязательства — дежурному смены, с причиной.
func TestTransferBrokenGoesToDutyOfficer(t *testing.T) {
	o := opened(domain.OriginPlanProposal, "")
	d := domain.OnTransferBroken(obligationID, requestID, "QUARANTINE", &o)
	if d.Kind != domain.KindTransferBroken || d.To.Role != domain.RoleDuty {
		t.Fatalf("черновик %+v: ожидалось нарушение дежурному", d)
	}
	if d.Facts.Reason != "QUARANTINE" || d.Facts.TargetClinicID == nil || *d.Facts.TargetClinicID != 2 {
		t.Fatalf("сведения не перенесены: %+v", d.Facts)
	}
}

// Запись без адресата не ждёт отправки, а сразу отказ: ИТ-служба видит её в журнале (ADR-0021).
func TestNewNotificationWithoutAddressFails(t *testing.T) {
	now := deadline.Add(-time.Hour)
	d, _ := domain.OnObligationOpened(opened(domain.OriginDoctorChoice, ""))
	n := domain.NewNotification(uuid.New(), uuid.New(), d, "", "тема", now)
	if n.Status != domain.StatusFailed || n.LastError == "" {
		t.Fatalf("без адреса ожидался FAILED с причиной, получено %s %q", n.Status, n.LastError)
	}
	ok := domain.NewNotification(uuid.New(), uuid.New(), d, "c001.transfusion@demo.blood-supply.local", "тема", now)
	if ok.Status != domain.StatusPending || !ok.NextAttemptAt.Equal(now) || ok.Attempts != 0 {
		t.Fatalf("с адресом ожидалась PENDING к немедленной отправке, получено %+v", ok)
	}
}
