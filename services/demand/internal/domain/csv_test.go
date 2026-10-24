package domain

import (
	"strings"
	"testing"
)

const profileHeader = "district_code;population;share_O+;share_O-;share_A+;share_A-;share_B+;share_B-;share_AB+;share_AB-;data_source;period_start;period_end"

// Доли строки профиля: сумма ровно 1.
const okShares = "0,33;0,04;0,35;0,04;0,15;0,02;0,06;0,01"

func TestParseFileKind(t *testing.T) {
	for _, k := range []FileKind{FileKindDistrictProfile, FileKindConsumptionHistory, FileKindClinicShare} {
		got, err := ParseFileKind(string(k))
		if err != nil || got != k {
			t.Fatalf("ParseFileKind(%q) = %q, %v", k, got, err)
		}
	}
	if _, err := ParseFileKind("PATIENTS"); err == nil {
		t.Fatal("неизвестный вид файла должен отклоняться")
	}
}

func TestParseDistrictProfiles_ValidFileAccepted(t *testing.T) {
	data := append([]byte("\xef\xbb\xbf"), csvOf(
		profileHeader,
		"D01;770000;"+okShares+";ДЕМО: статистика;2026-01-01;2026-12-31",
		"",
		" D02 ; 1180000 ;0.32;0.05;0.35;0.04;0.15;0.02;0.06;0.01;ДЕМО: перепись;2026-02-01;2027-01-31",
	)...)
	f := ParseDistrictProfiles(data, testRef(t))
	if f.Status != StatusAccepted || f.FileError != "" || len(f.RowErrors) != 0 {
		t.Fatalf("ожидали ACCEPTED без ошибок, получили %s, %q, %+v", f.Status, f.FileError, f.RowErrors)
	}
	if f.TotalRows != 2 || f.AcceptedRows != 2 || f.RejectedRows() != 0 || len(f.Profiles) != 2 {
		t.Fatalf("строк %d, принято %d, отклонено %d, профилей %d", f.TotalRows, f.AcceptedRows, f.RejectedRows(), len(f.Profiles))
	}
	p := f.Profiles[0]
	if p.Row != 2 || p.DistrictID != 1 || p.DistrictCode != "D01" || p.Population != 770000 {
		t.Fatalf("первый профиль: %+v", p)
	}
	if p.GroupShares["O+"].TenThousandths() != 3300 || p.GroupShares["AB-"].TenThousandths() != 100 {
		t.Fatalf("доли групп: %+v", p.GroupShares)
	}
	if p.DataSource != "ДЕМО: статистика" || p.Period.Start != date(2026, 1, 1) || p.Period.End != date(2026, 12, 31) {
		t.Fatalf("источник и период: %+v", p)
	}
	// Пустая строка пропущена, номер строки — физический номер в файле.
	if f.Profiles[1].Row != 4 || f.Profiles[1].DistrictID != 2 {
		t.Fatalf("второй профиль: %+v", f.Profiles[1])
	}
	if f.DataSource != "ДЕМО: статистика; ДЕМО: перепись" {
		t.Fatalf("сводный источник %q", f.DataSource)
	}
	if f.Period.Start != date(2026, 1, 1) || f.Period.End != date(2027, 1, 31) {
		t.Fatalf("сводный период %+v", f.Period)
	}
}

// FR016, FR017: сумма долей AB0/Rh района вне допуска — строка отклоняется.
func TestParseDistrictProfiles_GroupShareSumOutsideToleranceRejected(t *testing.T) {
	cases := []struct {
		name   string
		shares string
		ok     bool
	}{
		{"сумма 1", okShares, true},
		{"сумма 1,2", "0,53;0,04;0,35;0,04;0,15;0,02;0,06;0,01", false},
		{"сумма 1,0001 — на границе допуска", "0,3301;0,04;0,35;0,04;0,15;0,02;0,06;0,01", true},
		{"сумма 0,9999 — на границе допуска", "0,3299;0,04;0,35;0,04;0,15;0,02;0,06;0,01", true},
		{"сумма 1,0002", "0,3302;0,04;0,35;0,04;0,15;0,02;0,06;0,01", false},
		{"сумма 0,9998", "0,3298;0,04;0,35;0,04;0,15;0,02;0,06;0,01", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := ParseDistrictProfiles(csvOf(profileHeader, "D01;770000;"+c.shares+";ДЕМО;2026-01-01;2026-12-31"), testRef(t))
			if c.ok {
				if f.Status != StatusAccepted {
					t.Fatalf("ожидали ACCEPTED, получили %s: %+v", f.Status, f.RowErrors)
				}
				return
			}
			if f.Status != StatusRejected || len(f.Profiles) != 0 {
				t.Fatalf("ожидали REJECTED без профилей, получили %s, %d", f.Status, len(f.Profiles))
			}
			requireRowError(t, f.Check, 2, "share_*", "вне допуска 1 ± 0,0001")
		})
	}
}

func TestParseDistrictProfiles_BrokenRowsReportedPerRow(t *testing.T) {
	data := csvOf(
		profileHeader,
		"D01;770000;"+okShares+";ДЕМО;2026-01-01;2026-12-31",                        // 2: принята
		"D99;100;"+okShares+";ДЕМО;2026-01-01;2026-12-31",                           // 3: неизвестный район
		"D02;abc;"+okShares+";ДЕМО;2026-01-01;2026-12-31",                           // 4: население не число
		"D03;0;"+okShares+";ДЕМО;2026-01-01;2026-12-31",                             // 5: население 0
		"D04;100;"+okShares+";;2026-01-01;2026-12-31",                               // 6: нет источника
		"D05;100;"+okShares+";ДЕМО;;",                                               // 7: нет периода
		"D06;100;"+okShares+";ДЕМО;2026-12-31;2026-01-01",                           // 8: конец раньше начала
		"D02;100;"+okShares+";ДЕМО;01.01.2026;2026-12-31",                           // 9: дата не в формате
		"D03;100;1,5;0;0;0;0;0;0;0;ДЕМО;2026-01-01;2026-12-31",                      // 10: доля больше 1
		"D04;100;NaN;0,04;0,35;0,04;0,15;0,02;0,06;0,01;ДЕМО;2026-01-01;2026-12-31", // 11: доля не число
		"D01;770000;"+okShares+";ДЕМО;2026-01-01;2026-12-31",                        // 12: район повторяется
		"D05;100;"+okShares, // 13: не хватает полей
		"D06;3000000000;"+okShares+";ДЕМО;2026-01-01;2026-12-31",                  // 14: население вне integer
		";100;"+okShares+";ДЕМО;2026-01-01;2026-12-31",                            // 15: район не указан
		"D06;100;"+okShares+";"+strings.Repeat("я", 257)+";2026-01-01;2026-12-31", // 16: длинный источник
	)
	f := ParseDistrictProfiles(data, testRef(t))
	if f.Status != StatusPartial {
		t.Fatalf("ожидали PARTIAL, получили %s (%q)", f.Status, f.FileError)
	}
	if f.TotalRows != 15 || f.AcceptedRows != 1 || f.RejectedRows() != 14 {
		t.Fatalf("строк %d, принято %d, отклонено %d: %+v", f.TotalRows, f.AcceptedRows, f.RejectedRows(), f.RowErrors)
	}
	requireRowError(t, f.Check, 3, "district_code", "неизвестный район «D99»")
	requireRowError(t, f.Check, 4, "population", "целым числом")
	requireRowError(t, f.Check, 5, "population", "больше 0")
	requireRowError(t, f.Check, 6, "data_source", "не заполнено")
	requireRowError(t, f.Check, 7, "period_start", "не заполнено")
	requireRowError(t, f.Check, 7, "period_end", "не заполнено")
	requireRowError(t, f.Check, 8, "period_end", "раньше начала")
	requireRowError(t, f.Check, 9, "period_start", "ГГГГ-ММ-ДД")
	requireRowError(t, f.Check, 10, "share_O+", "от 0 до 1")
	requireRowError(t, f.Check, 11, "share_O+", "десятичная дробь")
	requireRowError(t, f.Check, 12, "district_code", "уже указан в строке 2")
	requireRowError(t, f.Check, 13, "", "полей 10, ожидается 13")
	requireRowError(t, f.Check, 14, "population", "не больше 2147483647")
	requireRowError(t, f.Check, 15, "district_code", "не заполнено")
	requireRowError(t, f.Check, 16, "data_source", "не длиннее 256")

	reasons := f.RowReasons()
	if len(reasons) != 14 || reasons[0].Row != 3 {
		t.Fatalf("причины по строкам: %+v", reasons)
	}
	// Одна запись file_row_error на строку: причины склеены.
	var seven string
	for _, r := range reasons {
		if r.Row == 7 {
			seven = r.Reason
		}
	}
	if !strings.Contains(seven, "period_start: не заполнено") || !strings.Contains(seven, "; period_end: не заполнено") {
		t.Fatalf("причина строки 7: %q", seven)
	}
}

func TestParseFile_FileLevelRejections(t *testing.T) {
	ref := testRef(t)
	cases := []struct {
		name     string
		data     []byte
		contains string
	}{
		{"не UTF-8", []byte("district_code;population\n\xff\xfe;1\n"), "UTF-8"},
		{"пустой файл", []byte(""), "пуст"},
		{"только заголовок", csvOf(profileHeader), "нет строк данных"},
		{"нет колонки", csvOf(strings.Replace(profileHeader, ";population", "", 1), "D01;"+okShares+";ДЕМО;2026-01-01;2026-12-31"), "нет колонки «population»"},
		{"лишняя колонка", csvOf(profileHeader+";patient_name", "D01;770000;"+okShares+";ДЕМО;2026-01-01;2026-12-31;x"), "неизвестная колонка «patient_name»"},
		{"колонка дважды", csvOf(profileHeader+";population", "D01;770000;"+okShares+";ДЕМО;2026-01-01;2026-12-31;1"), "колонка «population» повторяется"},
		{"разделитель запятая", csvOf(strings.ReplaceAll(profileHeader, ";", ","), "D01,770000"), "нет колонки"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := ParseDistrictProfiles(c.data, ref)
			if f.Status != StatusRejected {
				t.Fatalf("ожидали REJECTED, получили %s", f.Status)
			}
			if !strings.Contains(f.FileError, c.contains) {
				t.Fatalf("ошибка файла %q не содержит %q", f.FileError, c.contains)
			}
			if len(f.Profiles) != 0 {
				t.Fatal("из отклонённого файла не принимаются строки")
			}
		})
	}
}

const historyHeader = "component_kind_code;period_start;period_end;consumed_volume_ml;data_source"

func TestParseConsumptionHistory(t *testing.T) {
	data := csvOf(
		historyHeader,
		"RBC;2025-01-01;2025-06-30;1810000;ДЕМО: отчёт", // 2: принята
		"RBC;2025-07-01;2025-12-31;1840000;ДЕМО: отчёт", // 3: принята, смежный период
		"CRYO;2025-01-01;2025-12-31;0;ДЕМО: отчёт",      // 4: принята, нулевой расход допустим
		";2025-01-01;2025-12-31;100;ДЕМО",               // 5: нет вида (FR045)
		"FFP;;;100;ДЕМО",                                // 6: нет периода (FR045)
		"XYZ;2025-01-01;2025-12-31;100;ДЕМО",            // 7: неизвестный вид
		"FFP;2025-01-01;2025-12-31;100,5;ДЕМО",          // 8: дробный объём
		"FFP;2025-01-01;2025-12-31;-1;ДЕМО",             // 9: отрицательный объём
		"RBC;2025-06-01;2025-07-31;100;ДЕМО",            // 10: пересекается с 2 и 3
		"PLT;2025-01-01;2025-12-31;1000000000000;ДЕМО",  // 11: объём вне numeric(14,2)
		"PLT;2025-01-01;2025-12-31;5000;",               // 12: нет источника
	)
	f := ParseConsumptionHistory(data, testRef(t))
	if f.Status != StatusPartial || f.AcceptedRows != 3 || f.RejectedRows() != 8 {
		t.Fatalf("статус %s, принято %d, отклонено %d: %+v", f.Status, f.AcceptedRows, f.RejectedRows(), f.RowErrors)
	}
	r := f.Records[0]
	if r.Row != 2 || r.KindCode != "RBC" || r.ConsumedVolumeML != 1_810_000 || r.Source != HistorySourceFile ||
		r.Period.Start != date(2025, 1, 1) || r.Period.End != date(2025, 6, 30) || r.DataSource != "ДЕМО: отчёт" {
		t.Fatalf("первая запись: %+v", r)
	}
	requireRowError(t, f.Check, 5, "component_kind_code", "не заполнено")
	requireRowError(t, f.Check, 6, "period_start", "не заполнено")
	requireRowError(t, f.Check, 7, "component_kind_code", "неизвестный вид компонента «XYZ»")
	requireRowError(t, f.Check, 8, "consumed_volume_ml", "целым числом мл")
	requireRowError(t, f.Check, 9, "consumed_volume_ml", "целым числом мл")
	requireRowError(t, f.Check, 10, "period_start", "пересекается с периодом строки 2")
	requireRowError(t, f.Check, 11, "consumed_volume_ml", "не больше 999999999999")
	requireRowError(t, f.Check, 12, "data_source", "не заполнено")
}

func TestConsumptionHistoryFile_RejectRecords(t *testing.T) {
	f := ParseConsumptionHistory(csvOf(
		historyHeader,
		"RBC;2025-01-01;2025-12-31;100;ДЕМО",
		"FFP;2025-01-01;2025-12-31;100;ДЕМО",
	), testRef(t))
	if f.Status != StatusAccepted {
		t.Fatalf("статус %s", f.Status)
	}
	f.RejectRecords(func(r ConsumptionRecord) (string, string, bool) {
		return columnStart, "период уже загружен", r.KindCode == "RBC"
	})
	if f.Status != StatusPartial || len(f.Records) != 1 || f.Records[0].KindCode != "FFP" || f.AcceptedRows != 1 {
		t.Fatalf("после отклонения: %s, %+v", f.Status, f.Records)
	}
	requireRowError(t, f.Check, 2, "period_start", "уже загружен")
	if f.DataSource != "ДЕМО" || f.Period.Start != date(2025, 1, 1) {
		t.Fatalf("сводка потеряна: %q %+v", f.DataSource, f.Period)
	}
	f.RejectRecords(func(ConsumptionRecord) (string, string, bool) { return columnStart, "уже загружен", true })
	if f.Status != StatusRejected || f.RejectedRows() != 2 {
		t.Fatalf("все строки отклонены: %s, %d", f.Status, f.RejectedRows())
	}
}

const shareHeader = "district_code;clinic_code;clinic_share;basis;data_source;period_start;period_end"

func shareRow(district, clinic, share string) string {
	return district + ";" + clinic + ";" + share + ";ДЕМО: схема маршрутизации;ДЕМО: решение;2026-01-01;2026-12-31"
}

func TestParseClinicShares_ValidFileAccepted(t *testing.T) {
	f := ParseClinicShares(csvOf(
		shareHeader,
		shareRow("D01", "C001", "0,7"),
		shareRow("D01", "C002", "0,3"),
		shareRow("D02", "C002", "1"),
	), testRef(t))
	if f.Status != StatusAccepted || len(f.Shares) != 3 {
		t.Fatalf("статус %s, долей %d: %+v %q", f.Status, len(f.Shares), f.RowErrors, f.FileError)
	}
	s := f.Shares[0]
	if s.Row != 2 || s.DistrictID != 1 || s.ClinicID != 1 || s.Share.TenThousandths() != 7000 || s.Basis != "ДЕМО: схема маршрутизации" {
		t.Fatalf("первая доля: %+v", s)
	}
	if f.Basis != "ДЕМО: схема маршрутизации" || f.ValidFrom != date(2026, 1, 1) {
		t.Fatalf("основание %q, действует с %v", f.Basis, f.ValidFrom)
	}
}

// Приёмка задачи 04: файл с суммой долей района 1,2 отклоняется.
// Проверка «тест действительно проверяет»: без сверки суммы этот тест падает.
func TestParseClinicShares_SumOneAndTwoTenthsRejected(t *testing.T) {
	f := ParseClinicShares(csvOf(
		shareHeader,
		shareRow("D01", "C001", "0,7"),
		shareRow("D01", "C002", "0,5"),
	), testRef(t))
	if f.Status != StatusRejected {
		t.Fatalf("файл с суммой долей 1,2 должен быть отклонён, статус %s", f.Status)
	}
	if len(f.Shares) != 0 || f.AcceptedRows != 0 || f.RejectedRows() != 2 {
		t.Fatalf("принято долей %d, принято строк %d, отклонено %d", len(f.Shares), f.AcceptedRows, f.RejectedRows())
	}
	requireRowError(t, f.Check, 2, "clinic_share", "сумма долей района D01 по клиникам 1,2000")
	requireRowError(t, f.Check, 3, "clinic_share", "вне допуска 1 ± 0,0001")
}

func TestParseClinicShares_DistrictAcceptedWholeOrNotAtAll(t *testing.T) {
	f := ParseClinicShares(csvOf(
		shareHeader,
		shareRow("D01", "C001", "0,7"), // 2
		shareRow("D01", "C099", "0,3"), // 3: неизвестная клиника → у района остаётся 0,7
		shareRow("D02", "C002", "0,5"), // 4
		shareRow("D02", "C003", "0,5"), // 5
	), testRef(t))
	if f.Status != StatusPartial || len(f.Shares) != 2 {
		t.Fatalf("статус %s, долей %d: %+v", f.Status, len(f.Shares), f.RowErrors)
	}
	requireRowError(t, f.Check, 3, "clinic_code", "неизвестная клиника «C099»")
	requireRowError(t, f.Check, 2, "district_code", "доли района D01 отклонены целиком: в строке 3")
	for _, s := range f.Shares {
		if s.DistrictID != 2 {
			t.Fatalf("принята доля района %d, ожидали только D02", s.DistrictID)
		}
	}
	if f.DataSource != "ДЕМО: решение" || f.Period.Start != date(2026, 1, 1) {
		t.Fatalf("сводка по принятым строкам: %q %+v", f.DataSource, f.Period)
	}
}

// Ревью: строки 0,6 и 0,4 в сумме дают 1, но третья строка района (0,2) отклонена из-за пустого
// основания — район отклоняется целиком, а не принимается по «оставшейся» сумме.
func TestParseClinicShares_AnyRejectedRowRejectsDistrict(t *testing.T) {
	f := ParseClinicShares(csvOf(
		shareHeader,
		shareRow("D01", "C001", "0,6"),             // 2
		shareRow("D01", "C002", "0,4"),             // 3
		"D01;C003;0,2;;ДЕМО;2026-01-01;2026-12-31", // 4: нет основания
	), testRef(t))
	if f.Status != StatusRejected || len(f.Shares) != 0 || f.RejectedRows() != 3 {
		t.Fatalf("статус %s, долей %d, отклонено %d: %+v", f.Status, len(f.Shares), f.RejectedRows(), f.RowErrors)
	}
	requireRowError(t, f.Check, 4, "basis", "не заполнено")
	requireRowError(t, f.Check, 2, "district_code", "в строке 4")
	requireRowError(t, f.Check, 3, "district_code", "в строке 4")
	if f.DataSource != "" || !f.Period.IsZero() {
		t.Fatalf("у отклонённого файла нет сводки: %q %+v", f.DataSource, f.Period)
	}
}

func TestParseClinicShares_SumToleranceBoundaries(t *testing.T) {
	cases := []struct {
		a, b string
		ok   bool
	}{
		{"0,5", "0,5", true},
		{"0,5001", "0,5", true},      // 1,0001
		{"0,4999", "0,5", true},      // 0,9999
		{"0,5002", "0,5", false},     // 1,0002
		{"0,4998", "0,5", false},     // 0,9998
		{"0,49995", "0,5", true},     // 0,49995 → 0,5000 после округления
		{"0,33333", "0,66666", true}, // 0,3333 + 0,6667 = 1
	}
	for _, c := range cases {
		t.Run(c.a+"+"+c.b, func(t *testing.T) {
			f := ParseClinicShares(csvOf(shareHeader, shareRow("D01", "C001", c.a), shareRow("D01", "C002", c.b)), testRef(t))
			if got := f.Status == StatusAccepted; got != c.ok {
				t.Fatalf("статус %s, ожидали принятие=%v: %+v", f.Status, c.ok, f.RowErrors)
			}
		})
	}
}

func TestParseClinicShares_BrokenRows(t *testing.T) {
	data := csvOf(
		shareHeader,
		shareRow("D01", "C001", "1"),             // 2: принята
		shareRow("D06", "C001", "1"),             // 3: отклонена вместе с районом D06 (строка 9)
		shareRow("D02", "C002", "0"),             // 4: доля 0
		shareRow("D03", "C002", "0,00004"),       // 5: после округления 0
		shareRow("D04", "C002", "1,5"),           // 6: больше 1
		"D05;C002;1;;ДЕМО;2026-01-01;2026-12-31", // 7: нет основания
		shareRow("D99", "C002", "1"),             // 8: неизвестный район
		shareRow("D06", "C001", "0,2"),           // 9: пара повторяется
	)
	f := ParseClinicShares(data, testRef(t))
	if f.Status != StatusPartial || len(f.Shares) != 1 || f.Shares[0].Row != 2 {
		t.Fatalf("статус %s, долей %d: %+v", f.Status, len(f.Shares), f.RowErrors)
	}
	requireRowError(t, f.Check, 9, "clinic_code", "пара район D06 — клиника C001 уже указана в строке 3")
	requireRowError(t, f.Check, 3, "district_code", "доли района D06 отклонены целиком: в строке 9")
	requireRowError(t, f.Check, 4, "clinic_share", "больше 0 и не больше 1")
	requireRowError(t, f.Check, 5, "clinic_share", "больше 0 и не больше 1")
	requireRowError(t, f.Check, 6, "clinic_share", "больше 0 и не больше 1")
	requireRowError(t, f.Check, 7, "basis", "не заполнено")
	requireRowError(t, f.Check, 8, "district_code", "неизвестный район «D99»")
}

func TestParse_BrokenQuotesRejectOnlyThatRow(t *testing.T) {
	f := ParseClinicShares(csvOf(
		shareHeader,
		`D01;C0"01;1;ДЕМО;ДЕМО;2026-01-01;2026-12-31`,
		shareRow("D02", "C002", "1"),
	), testRef(t))
	if f.Status != StatusPartial || len(f.Shares) != 1 || f.Shares[0].Row != 3 {
		t.Fatalf("статус %s, долей %d: %+v", f.Status, len(f.Shares), f.RowErrors)
	}
	requireRowError(t, f.Check, 2, "", "кавычк")
}

// Незакрытая кавычка: encoding/csv дочитывает до конца файла одной ошибкой, строки после неё пропали бы
// молча — файл отклоняется целиком.
func TestParse_UnclosedQuoteRejectsFile(t *testing.T) {
	cases := map[string][]byte{
		"в середине файла": csvOf(
			shareHeader,
			shareRow("D01", "C001", "1"),
			`D02;"C002;1;ДЕМО;ДЕМО;2026-01-01;2026-12-31`,
			shareRow("D03", "C003", "1"),
			shareRow("D04", "C004", "1"),
		),
		"в последней строке": csvOf(
			shareHeader,
			shareRow("D01", "C001", "1"),
			`D02;"C002;1;ДЕМО;ДЕМО;2026-01-01;2026-12-31`,
		),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			f := ParseClinicShares(data, testRef(t))
			if f.Status != StatusRejected || len(f.Shares) != 0 {
				t.Fatalf("статус %s, долей %d", f.Status, len(f.Shares))
			}
			if !strings.Contains(f.FileError, "в строке 3 не закрыта кавычка") {
				t.Fatalf("причина: %q", f.FileError)
			}
		})
	}
}

func TestParse_CRLFLineEndings(t *testing.T) {
	data := []byte(shareHeader + "\r\n" + shareRow("D01", "C001", "1") + "\r\n" + shareRow("D02", "C099", "1") + "\r\n")
	f := ParseClinicShares(data, testRef(t))
	if f.Status != StatusPartial || len(f.Shares) != 1 {
		t.Fatalf("статус %s: %+v", f.Status, f.RowErrors)
	}
	if f.Shares[0].Period.End != date(2026, 12, 31) || f.Shares[0].Basis != "ДЕМО: схема маршрутизации" {
		t.Fatalf("последнее поле строки с CRLF: %+v", f.Shares[0])
	}
	requireRowError(t, f.Check, 3, "clinic_code", "«C099»")
}

func TestParse_QuotedFieldWithSeparator(t *testing.T) {
	f := ParseClinicShares(csvOf(
		shareHeader,
		`D01;C001;1;"ДЕМО: схема; редакция 2";"ДЕМО: решение ""ДЗМ""";2026-01-01;2026-12-31`,
	), testRef(t))
	if f.Status != StatusAccepted {
		t.Fatalf("статус %s: %+v %q", f.Status, f.RowErrors, f.FileError)
	}
	if f.Shares[0].Basis != "ДЕМО: схема; редакция 2" || f.Shares[0].DataSource != `ДЕМО: решение "ДЗМ"` {
		t.Fatalf("поля в кавычках: %+v", f.Shares[0])
	}
}

// Поле в кавычках с переводом строки занимает две строки файла: ошибка следующей записи указывает
// её настоящую строку в файле, а не порядковый номер записи.
func TestParse_QuotedFieldWithNewlineKeepsFileLineNumbers(t *testing.T) {
	f := ParseClinicShares(csvOf(
		shareHeader,
		"D01;C001;1;\"ДЕМО: схема\nмаршрутизации\";ДЕМО;2026-01-01;2026-12-31", // строки 2–3
		shareRow("D02", "C099", "1"), // строка 4
	), testRef(t))
	if f.Status != StatusPartial || len(f.Shares) != 1 || f.Shares[0].Row != 2 {
		t.Fatalf("статус %s: %+v", f.Status, f.RowErrors)
	}
	if f.Shares[0].Basis != "ДЕМО: схема\nмаршрутизации" {
		t.Fatalf("основание: %q", f.Shares[0].Basis)
	}
	requireRowError(t, f.Check, 4, "clinic_code", "«C099»")
}

func TestParse_SeparatorOnlyRowsSkipped(t *testing.T) {
	f := ParseClinicShares(csvOf(shareHeader, shareRow("D01", "C001", "1"), ";;;;;;", " ; ;;;;; "), testRef(t))
	if f.Status != StatusAccepted || f.TotalRows != 1 {
		t.Fatalf("статус %s, строк %d: %+v", f.Status, f.TotalRows, f.RowErrors)
	}
}

func TestParse_LimitsRejectFile(t *testing.T) {
	ref := testRef(t)
	big := make([]byte, MaxFileBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	if f := ParseClinicShares(big, ref); f.Status != StatusRejected || !strings.Contains(f.FileError, "больше 5 МиБ") {
		t.Fatalf("размер: %s %q", f.Status, f.FileError)
	}

	lines := []string{shareHeader}
	for i := 0; i <= MaxDataRows; i++ {
		// Короткая строка изолирует лимит количества от отдельного лимита 5 МиБ.
		lines = append(lines, "D01;C001;1;D;D;2026-01-01;2026-12-31")
	}
	if f := ParseClinicShares(csvOf(lines...), ref); f.Status != StatusRejected || !strings.Contains(f.FileError, "больше 50000 строк") {
		t.Fatalf("число строк: %s %q", f.Status, f.FileError)
	}

	long := shareRow("D01", "C001", "1") + strings.Repeat("я", maxCellLen)
	if f := ParseClinicShares(csvOf(shareHeader, long), ref); f.Status != StatusRejected || !strings.Contains(f.FileError, "длиннее 1024") {
		t.Fatalf("длина поля: %s %q", f.Status, f.FileError)
	}
}

func TestParse_DateYearsLimited(t *testing.T) {
	f := ParseClinicShares(csvOf(shareHeader,
		"D01;C001;1;ДЕМО;ДЕМО;1899-12-31;2026-12-31",
		"D02;C002;1;ДЕМО;ДЕМО;2026-01-01;2101-01-01",
	), testRef(t))
	if f.Status != StatusRejected {
		t.Fatalf("статус %s", f.Status)
	}
	requireRowError(t, f.Check, 2, "period_start", "от 1900 до 2100")
	requireRowError(t, f.Check, 3, "period_end", "от 1900 до 2100")
}

func TestParse_LongValuesShortenedInMessages(t *testing.T) {
	code := strings.Repeat("Ж", 100)
	f := ParseClinicShares(csvOf(shareHeader, shareRow(code, "C001", "1")), testRef(t))
	e, ok := findRowError(f.RowErrors, 2, "district_code")
	if !ok {
		t.Fatalf("нет ошибки района: %+v", f.RowErrors)
	}
	if strings.Contains(e.Message, code) || !strings.Contains(e.Message, strings.Repeat("Ж", 64)+"…") {
		t.Fatalf("значение не обрезано до 64 символов: %q", e.Message)
	}
}

func TestNewReference_RejectsDuplicates(t *testing.T) {
	kinds := []KindRef{{Code: "RBC", IsGroupApplicable: true}}
	if _, err := NewReference([]DistrictRef{{1, "D01"}, {2, "D01"}}, []ClinicRef{{1, "C001"}}, kinds, testGroups); err == nil {
		t.Fatal("повтор кода района — ошибка")
	}
	if _, err := NewReference([]DistrictRef{{1, "D01"}}, []ClinicRef{{1, "C001"}, {1, "C002"}}, kinds, testGroups); err == nil {
		t.Fatal("повтор идентификатора клиники — ошибка")
	}
	if _, err := NewReference([]DistrictRef{{1, "D01"}}, []ClinicRef{{1, "C001"}}, kinds, []string{"O+", "O+"}); err == nil {
		t.Fatal("повтор группы — ошибка")
	}
	if _, err := NewReference([]DistrictRef{{1, "D01"}}, []ClinicRef{{1, "C001"}}, nil, testGroups); err == nil {
		t.Fatal("пустой справочник видов — ошибка")
	}
}

func TestParse_NilReferenceRejectsFile(t *testing.T) {
	f := ParseClinicShares(csvOf(shareHeader, shareRow("D01", "C001", "1")), nil)
	if f.Status != StatusRejected || f.FileError == "" {
		t.Fatalf("без справочника файл отклоняется: %s %q", f.Status, f.FileError)
	}
}
