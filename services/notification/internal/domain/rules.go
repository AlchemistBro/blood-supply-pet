package domain

import "github.com/google/uuid"

// OnObligationOpened решает, нужно ли письмо оператору клиники-источника о новом задании (FR044).
//
// Письмо уходит только по заданию, созданному выбором врача: задания плана пополнения идут своим порядком и
// срочными не являются. Плановая срочность письма не порождает. В ObligationOpened v1 срочности нет — тогда
// задание по выбору врача считается срочным (FR044 называет его «срочным заданием»). Источник, совпадающий
// с получателем, — местный вариант: по FR044 письмо не отправляется.
func OnObligationOpened(o Obligation) (Draft, bool) {
	if o.Origin != OriginDoctorChoice || o.Urgency == UrgencyPlanned || o.SourceClinicID == o.TargetClinicID {
		return Draft{}, false
	}
	source := o.SourceClinicID
	return Draft{
		Kind:  KindUrgentTask,
		To:    Recipient{Role: RoleOperator, ClinicID: &source},
		Facts: factsOf(o.ObligationID, o.RequestID, "", &o),
	}, true
}

// OnEscalation — письмо дежурному смены об эскалации. Сведения о задании берутся из проекции, если она есть;
// без неё письмо всё равно уходит с номером задания и причиной: дежурный найдёт задание по ссылке.
func OnEscalation(obligationID, requestID uuid.UUID, reason string, o *Obligation) Draft {
	return Draft{Kind: KindEscalation, To: Recipient{Role: RoleDuty}, Facts: factsOf(obligationID, requestID, reason, o)}
}

// OnTransferBroken — письмо дежурному смены о нарушении обязательства на передачу.
func OnTransferBroken(obligationID, requestID uuid.UUID, reason string, o *Obligation) Draft {
	return Draft{Kind: KindTransferBroken, To: Recipient{Role: RoleDuty}, Facts: factsOf(obligationID, requestID, reason, o)}
}

func factsOf(obligationID, requestID uuid.UUID, reason string, o *Obligation) Facts {
	f := Facts{ObligationID: obligationID, RequestID: requestID, Reason: reason}
	if o == nil {
		return f
	}
	source, target, qty, deadline := o.SourceClinicID, o.TargetClinicID, o.RequestedQty, o.Deadline
	f.SourceClinicID, f.TargetClinicID = &source, &target
	f.ComponentKind = o.ComponentKind
	f.Quantity = &qty
	f.Urgency = o.Urgency
	f.Deadline = &deadline
	return f
}
