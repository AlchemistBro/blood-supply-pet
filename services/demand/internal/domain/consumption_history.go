package domain

import (
	"fmt"
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
	var out ConsumptionHistoryFile
	var sum summary
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
				c.fail(columnKind, fmt.Sprintf("неизвестный вид компонента «%s»", code))
			}
		}
		p, periodOK := c.period()
		r.Period = p
		if v, ok := c.intInRange(columnConsumed, 0, maxConsumedML, "объём должен быть целым числом мл не меньше 0"); ok {
			r.ConsumedVolumeML = v
		}
		r.DataSource = c.text(columnDataSource)
		sum.add(r.DataSource, r.Period)

		if periodOK && r.KindCode != "" {
			for _, prev := range out.Records {
				if prev.KindCode == r.KindCode && prev.Period.Overlaps(r.Period) {
					c.fail(columnStart, fmt.Sprintf("период пересекается с периодом строки %d того же вида", prev.Row))
					break
				}
			}
		}
		if !c.ok() {
			errs = append(errs, c.errs...)
			continue
		}
		out.Records = append(out.Records, r)
	}
	finish(&out.Check, len(rows), len(out.Records), errs, sum)
	return out
}
