package server

import (
	"io"
	"net/http"
)

// Шаблоны синхронизированы с docs/ui/csv-formats.md; только синтетические агрегаты.
var demandTemplates = map[string]string{
	"DISTRICT_PROFILE":    "district_code;population;share_O+;share_O-;share_A+;share_A-;share_B+;share_B-;share_AB+;share_AB-;data_source;period_start;period_end\nD01;770000;0.33;0.04;0.35;0.04;0.15;0.02;0.06;0.01;ДЕМО: статистика района;2026-01-01;2026-12-31\n",
	"CONSUMPTION_HISTORY": "component_kind_code;period_start;period_end;consumed_volume_ml;data_source\nRBC;2025-01-01;2025-12-31;36500000;ДЕМО: отчёт о расходе\n",
	"CLINIC_SHARE":        "district_code;clinic_code;clinic_share;basis;data_source;period_start;period_end\nD01;C017;0.6;ДЕМО: схема маршрутизации;ДЕМО: решение ДЗМ;2026-01-01;2026-12-31\nD01;C001;0.4;ДЕМО: схема маршрутизации;ДЕМО: решение ДЗМ;2026-01-01;2026-12-31\n",
}

func (s *Server) demandCSVTemplate(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	body, ok := demandTemplates[kind]
	if !ok {
		s.renderError(w, r, 404, "Шаблон не найден", "Выберите один из трёх видов CSV.")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+kind+`.csv"`)
	_, _ = io.WriteString(w, body)
}
