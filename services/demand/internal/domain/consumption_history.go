package domain

import (
	"fmt"
	"sort"
)

// HistorySource — откуда история расхода (consumption_history.history_source).
type HistorySource string

// Источники истории: файл аналитика или согласованная выгрузка АИСТ-М (FR045, ConsumptionExportPublished).
const (
	HistorySourceFile HistorySource = "FILE"
	HistorySourceC01  HistorySource = "C01"
)

// ConsumptionRecord — O17 расход вида компонента за период по всей территории; база B(k, p) (FR019).
type ConsumptionRecord struct {
	Row              int
	KindCode         string
	Period           Period
	ConsumedVolumeML int64
	DataSource       string
	Source           HistorySource
}

// ConsumptionHistoryFile — итог разбора файла CONSUMPTION_HISTORY.
type ConsumptionHistoryFile struct {
	Check
	Records []ConsumptionRecord
}

const (
	columnKind     = "component_kind_code"
	columnConsumed = "consumed_volume_ml"
)

var historyColumns = []string{columnKind, columnStart, columnEnd, columnConsumed, columnDataSource}

// ParseConsumptionHistory разбирает и проверяет историю расхода (FR045): вид из справочника, период,
// объём в целых мл; периоды одного вида не пересекаются, иначе расход посчитался бы в базе дважды.
func ParseConsumptionHistory(data []byte, ref *Reference) ConsumptionHistoryFile {
	if ref == nil {
		return ConsumptionHistoryFile{Check: rejectFile(msgNoReference)}
	}
	rows, errs, fileErr := readCSV(data, historyColumns)
	if fileErr != "" {
		return ConsumptionHistoryFile{Check: rejectFile(fileErr)}
	}
	var candidates []ConsumptionRecord
	for _, row := range rows {
		if row.values == nil {
			continue
		}
		c := &rowCheck{row: row}
		r := ConsumptionRecord{Row: row.line, Source: HistorySourceFile}

		if code, ok := c.required(columnKind); ok {
			if k, known := ref.Kind(code); known {
				r.KindCode = k.Code
			} else {
				c.fail(columnKind, fmt.Sprintf("неизвестный вид компонента «%s»", shown(code)))
			}
		}
		r.Period, _ = c.period()
		if v, ok := c.intInRange(columnConsumed, 0, maxConsumedML, "объём должен быть целым числом мл не меньше 0"); ok {
			r.ConsumedVolumeML = v
		}
		r.DataSource = c.text(columnDataSource)
		if !c.ok() {
			errs = append(errs, c.errs...)
			continue
		}
		candidates = append(candidates, r)
	}
	accepted, overlapErrs := rejectOverlaps(candidates)
	errs = append(errs, overlapErrs...)
	out := ConsumptionHistoryFile{Records: accepted}
	finish(&out.Check, len(rows), len(accepted), errs, historySummary(accepted))
	return out
}

// rejectOverlaps отклоняет строки, чей период пересекается с более ранней (по началу периода) строкой
// того же вида. Сортировка по виду и началу периода: O(n log n), а не попарное сравнение.
// Порядок принятых строк — как в файле.
func rejectOverlaps(records []ConsumptionRecord) ([]ConsumptionRecord, []RowError) {
	idx := make([]int, len(records))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		x, y := records[idx[a]], records[idx[b]]
		if x.KindCode != y.KindCode {
			return x.KindCode < y.KindCode
		}
		if !x.Period.Start.Equal(y.Period.Start) {
			return x.Period.Start.Before(y.Period.Start)
		}
		return x.Row < y.Row
	})
	rejected := make([]bool, len(records))
	var errs []RowError
	last := -1 // принятая строка того же вида с самым поздним концом периода
	for _, i := range idx {
		r := records[i]
		if last >= 0 && records[last].KindCode == r.KindCode && !records[last].Period.End.Before(r.Period.Start) {
			rejected[i] = true
			errs = append(errs, RowError{Row: r.Row, Field: columnStart,
				Message: fmt.Sprintf("период пересекается с периодом строки %d того же вида", records[last].Row)})
			continue
		}
		if last < 0 || records[last].KindCode != r.KindCode || r.Period.End.After(records[last].Period.End) {
			last = i
		}
	}
	accepted := make([]ConsumptionRecord, 0, len(records))
	for i, r := range records {
		if !rejected[i] {
			accepted = append(accepted, r)
		}
	}
	return accepted, errs
}

func historySummary(records []ConsumptionRecord) summary {
	var sum summary
	for _, r := range records {
		sum.add(r.DataSource, r.Period)
	}
	return sum
}

// RejectRecords отклоняет принятые строки, для которых reject возвращает причину, и пересчитывает статус
// и сводку. Нужна приложению: конфликт с уже сохранённой историей виден только по базе.
func (f *ConsumptionHistoryFile) RejectRecords(reject func(ConsumptionRecord) (field, msg string, bad bool)) {
	kept := make([]ConsumptionRecord, 0, len(f.Records))
	errs := append([]RowError(nil), f.RowErrors...)
	for _, r := range f.Records {
		if field, msg, bad := reject(r); bad {
			errs = append(errs, RowError{Row: r.Row, Field: field, Message: msg})
			continue
		}
		kept = append(kept, r)
	}
	f.Records = kept
	finish(&f.Check, f.TotalRows, len(kept), errs, historySummary(kept))
}
