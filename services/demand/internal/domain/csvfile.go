package domain

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// FileKind — вид файла аналитика (statistics_file.file_kind).
type FileKind string

// Виды файлов из ПР6 (FR016, FR045, FR046).
const (
	FileKindDistrictProfile    FileKind = "DISTRICT_PROFILE"
	FileKindConsumptionHistory FileKind = "CONSUMPTION_HISTORY"
	FileKindClinicShare        FileKind = "CLINIC_SHARE"
)

// ParseFileKind проверяет вид файла, выбранный аналитиком.
func ParseFileKind(s string) (FileKind, error) {
	switch k := FileKind(s); k {
	case FileKindDistrictProfile, FileKindConsumptionHistory, FileKindClinicShare:
		return k, nil
	default:
		return "", fmt.Errorf("неизвестный вид файла %q: ожидается DISTRICT_PROFILE, CONSUMPTION_HISTORY или CLINIC_SHARE", s)
	}
}

// CheckStatus — результат проверки файла (statistics_file.check_status, FR017).
type CheckStatus string

// Статусы проверки файла.
const (
	StatusAccepted CheckStatus = "ACCEPTED"
	StatusPartial  CheckStatus = "PARTIAL"
	StatusRejected CheckStatus = "REJECTED"
)

// RowError — причина отклонения строки: номер строки файла (заголовок — строка 1), колонка и сообщение
// для аналитика. Field пуст, если ошибка относится ко всей строке (число полей, кавычки).
type RowError struct {
	Row     int
	Field   string
	Message string
}

func (e RowError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("строка %d: %s", e.Row, e.Message)
	}
	return fmt.Sprintf("строка %d, %s: %s", e.Row, e.Field, e.Message)
}

// RowReason — одна запись file_row_error: строка и все её причины через «; ».
type RowReason struct {
	Row    int
	Reason string
}

// Check — итог проверки файла. FileError непуст, если файл отклонён целиком.
// DataSource и Period — сводка для statistics_file по строкам с корректными источником и периодом.
type Check struct {
	Status       CheckStatus
	FileError    string
	TotalRows    int
	AcceptedRows int
	RowErrors    []RowError
	DataSource   string
	Period       Period
}

// RejectedRows — число отклонённых строк (statistics_file.rejected_rows).
func (c Check) RejectedRows() int {
	return len(c.RowReasons())
}

// RowReasons склеивает ошибки по строкам: в file_row_error одна запись на строку (UNIQUE row_no).
func (c Check) RowReasons() []RowReason {
	var out []RowReason
	idx := map[int]int{}
	for _, e := range c.RowErrors {
		part := e.Message
		if e.Field != "" {
			part = e.Field + ": " + e.Message
		}
		if i, ok := idx[e.Row]; ok {
			out[i].Reason += "; " + part
			continue
		}
		idx[e.Row] = len(out)
		out = append(out, RowReason{Row: e.Row, Reason: part})
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Row < out[b].Row })
	return out
}

// Ограничения из ПР6: varchar(256), integer, numeric(14,2).
const (
	maxTextLen       = 256
	maxPopulation    = 1<<31 - 1
	maxConsumedML    = 999_999_999_999
	msgEmpty         = "не заполнено"
	csvSeparator     = ';'
	sourceSeparator  = "; "
	columnDataSource = "data_source"
	columnStart      = "period_start"
	columnEnd        = "period_end"
)

// csvRow — строка данных с номером строки файла и значениями по именам колонок.
type csvRow struct {
	line   int
	values map[string]string
}

func (r csvRow) get(col string) string { return r.values[col] }

// readCSV читает файл по общим правилам docs/ui/csv-formats.md. Ошибка заголовка или кодировки
// возвращается как fileErr; битые строки — как ошибки строк, остальные строки читаются дальше.
func readCSV(data []byte, columns []string) (rows []csvRow, rowErrs []RowError, fileErr string) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		return nil, nil, "файл не в кодировке UTF-8: сохраните его как «CSV UTF-8»"
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil, "файл пуст"
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = csvSeparator
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, nil, "не удалось прочитать заголовок: " + csvErrorText(err)
	}
	if msg := checkHeader(header, columns); msg != "" {
		return nil, nil, msg
	}
	for i := range header {
		header[i] = strings.TrimSpace(header[i])
	}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				rowErrs = append(rowErrs, RowError{Row: pe.StartLine, Message: csvErrorText(err)})
				rows = append(rows, csvRow{line: pe.StartLine})
				continue
			}
			return nil, nil, "не удалось прочитать файл: " + err.Error()
		}
		line, _ := r.FieldPos(0)
		if len(rec) != len(header) {
			rowErrs = append(rowErrs, RowError{Row: line,
				Message: fmt.Sprintf("в строке полей %d, ожидается %d", len(rec), len(header))})
			rows = append(rows, csvRow{line: line})
			continue
		}
		values := make(map[string]string, len(header))
		for i, h := range header {
			values[h] = strings.TrimSpace(rec[i])
		}
		rows = append(rows, csvRow{line: line, values: values})
	}
	if len(rows) == 0 {
		return nil, nil, "в файле нет строк данных"
	}
	return rows, rowErrs, ""
}

// checkHeader сверяет заголовок с набором колонок вида файла. Лишние колонки запрещены: в системе нет
// полей для ПДн, и неизвестные данные не принимаются даже «на будущее».
func checkHeader(header, columns []string) string {
	want := make(map[string]bool, len(columns))
	for _, c := range columns {
		want[c] = true
	}
	seen := map[string]bool{}
	var problems []string
	if len(header) == 1 && strings.Contains(header[0], ",") {
		problems = append(problems, "разделитель полей — точка с запятой «;»")
	}
	for _, h := range header {
		h = strings.TrimSpace(h)
		switch {
		case seen[h]:
			problems = append(problems, fmt.Sprintf("колонка «%s» повторяется", h))
		case !want[h]:
			problems = append(problems, fmt.Sprintf("неизвестная колонка «%s»", h))
		}
		seen[h] = true
	}
	for _, c := range columns {
		if !seen[c] {
			problems = append(problems, fmt.Sprintf("нет колонки «%s»", c))
		}
	}
	if len(problems) == 0 {
		return ""
	}
	return "заголовок файла не соответствует формату (" + strings.Join(problems, "; ") +
		"); ожидаются колонки: " + strings.Join(columns, ";")
}

func csvErrorText(err error) string {
	var pe *csv.ParseError
	if errors.As(err, &pe) {
		switch {
		case errors.Is(pe.Err, csv.ErrBareQuote), errors.Is(pe.Err, csv.ErrQuote):
			return "нарушены кавычки: поле с кавычкой или «;» берётся в двойные кавычки, кавычка внутри удваивается"
		default:
			return "строка не разбирается как CSV"
		}
	}
	return err.Error()
}

// rowCheck копит ошибки одной строки.
type rowCheck struct {
	row  csvRow
	errs []RowError
}

func (c *rowCheck) fail(field, msg string) {
	c.errs = append(c.errs, RowError{Row: c.row.line, Field: field, Message: msg})
}

func (c *rowCheck) ok() bool { return len(c.errs) == 0 }

// required возвращает значение колонки или отмечает «не заполнено».
func (c *rowCheck) required(col string) (string, bool) {
	v := c.row.get(col)
	if v == "" {
		c.fail(col, msgEmpty)
		return "", false
	}
	return v, true
}

// text — обязательный текст не длиннее varchar(256).
func (c *rowCheck) text(col string) string {
	v, ok := c.required(col)
	if !ok {
		return ""
	}
	if utf8.RuneCountInString(v) > maxTextLen {
		c.fail(col, fmt.Sprintf("текст не длиннее %d символов", maxTextLen))
		return ""
	}
	return v
}

func (c *rowCheck) date(col string) (time.Time, bool) {
	v, ok := c.required(col)
	if !ok {
		return time.Time{}, false
	}
	t, err := parseDate(v)
	if err != nil {
		c.fail(col, err.Error())
		return time.Time{}, false
	}
	return t, true
}

// period — обязательный период актуальности строки (FR016, FR017, FR045).
func (c *rowCheck) period() (Period, bool) {
	start, okStart := c.date(columnStart)
	end, okEnd := c.date(columnEnd)
	if !okStart || !okEnd {
		return Period{}, false
	}
	p, err := NewPeriod(start, end)
	if err != nil {
		c.fail(columnEnd, err.Error())
		return Period{}, false
	}
	return p, true
}

// positiveInt — целое в диапазоне [lo, hi].
func (c *rowCheck) intInRange(col string, lo, hi int64, badFormat string) (int64, bool) {
	v, ok := c.required(col)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < lo {
		c.fail(col, badFormat)
		return 0, false
	}
	if n > hi {
		c.fail(col, fmt.Sprintf("должно быть не больше %d", hi))
		return 0, false
	}
	return n, true
}

// share — доля с округлением до 4 знаков; диапазон проверяет вызывающий.
func (c *rowCheck) share(col string) (Share, bool) {
	v, ok := c.required(col)
	if !ok {
		return 0, false
	}
	s, err := ParseShare(v)
	if err != nil {
		c.fail(col, err.Error())
		return 0, false
	}
	return s, true
}

// summary собирает источник и период файла по строкам с корректными источником и периодом.
type summary struct {
	sources []string
	seen    map[string]bool
	period  Period
}

func (s *summary) add(source string, p Period) {
	if source == "" || p.IsZero() {
		return
	}
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	if !s.seen[source] {
		s.seen[source] = true
		s.sources = append(s.sources, source)
	}
	if s.period.IsZero() {
		s.period = p
		return
	}
	if p.Start.Before(s.period.Start) {
		s.period.Start = p.Start
	}
	if p.End.After(s.period.End) {
		s.period.End = p.End
	}
}

// joinLimited склеивает различающиеся значения через «; » в пределах varchar(256).
func joinLimited(values []string) string {
	s := strings.Join(values, sourceSeparator)
	if utf8.RuneCountInString(s) <= maxTextLen {
		return s
	}
	r := []rune(s)
	return string(r[:maxTextLen-1]) + "…"
}

// finish выставляет статус по FR017: целиком принят, частично или отклонён.
func finish(c *Check, total, accepted int, errs []RowError, sum summary) {
	sort.SliceStable(errs, func(a, b int) bool { return errs[a].Row < errs[b].Row })
	c.TotalRows = total
	c.AcceptedRows = accepted
	c.RowErrors = errs
	c.DataSource = joinLimited(sum.sources)
	c.Period = sum.period
	switch {
	case accepted == 0:
		c.Status = StatusRejected
	case len(errs) > 0:
		c.Status = StatusPartial
	default:
		c.Status = StatusAccepted
	}
}

func rejectFile(msg string) Check {
	return Check{Status: StatusRejected, FileError: msg}
}

const msgNoReference = "справочники не загружены: проверка файла невозможна"
