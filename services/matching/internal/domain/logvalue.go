package domain

import "log/slog"

// redacted заменяет в журнале фенотип: это квазиидентификатор пациента и единицы, в логи он не попадает
// (CONTRIBUTING.md, правила кода; ADR-0011). Значения ограничений заявки (K1) тоже не пишутся — только их число.
const redacted = "[скрыто]"

func redact(s string) string {
	if s == "" {
		return ""
	}
	return redacted
}

// LogValue — заявка в журнале без требований фенотипа и без содержимого ограничений.
func (r Request) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("request_id", r.RequestID.String()),
		slog.Int64("clinic_id", r.ClinicID),
		slog.String("component_kind", r.ComponentKind),
		slog.Int("requested_units", r.RequestedUnits),
		slog.String("urgency", r.Urgency),
		slog.String("blood_group", r.BloodGroup),
		slog.String("phenotype", redact(r.Phenotype)),
		slog.Int("constraints", len(r.Constraints)),
	)
}

// LogValue — единица в журнале без фенотипа.
func (u Unit) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("unit_id", u.UnitID.String()),
		slog.Int64("clinic_id", u.ClinicID),
		slog.String("component_kind", u.ComponentKind),
		slog.String("blood_group", u.BloodGroup),
		slog.String("status", u.Status),
		slog.Time("expires_at", u.ExpiresAt),
		slog.String("phenotype", redact(u.Phenotype)),
	)
}

// LogValue — спецификация замены в журнале без фенотипа.
func (s ReplacementSpec) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("component_kind", s.ComponentKind),
		slog.String("blood_group", s.BloodGroup),
		slog.String("phenotype", redact(s.Phenotype)),
		slog.Time("min_expires_at", s.MinExpiresAt),
		slog.String("rule_code", s.RuleCode),
		slog.Int64("rule_version", s.RuleVersion),
	)
}

// LogValue — вариант в журнале: без объяснения (длинный текст для врача) и со спецификацией без фенотипа.
func (v Variant) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("rank", v.Rank),
		slog.Int64("source_clinic_id", v.SourceClinicID),
		slog.String("component_kind", v.ComponentKind),
		slog.String("blood_group", v.BloodGroup),
		slog.String("rule_code", v.Rule.Code),
		slog.Int64("rule_version", v.Rule.Version),
		slog.Int("available_units", v.AvailableUnits),
		slog.Int("units", len(v.UnitIDs)),
		slog.String("delivery", string(v.Delivery.Status)),
		slog.Bool("deficit_stock_used", v.IsDeficitStockUsed),
		slog.Any("spec", v.Spec),
	)
}
