package domain

import (
	"fmt"
	"time"
)

// ClinicShare — O16 доля потребности района w(r, c), относимая к клинике. Расчётная схема обслуживания,
// а не закрепление жителей (FR046).
type ClinicShare struct {
	Row          int
	DistrictID   int64
	DistrictCode string
	ClinicID     int64
	ClinicCode   string
	Share        Share
	Basis        string
	DataSource   string
	Period       Period
}

// ClinicShareFile — итог разбора файла CLINIC_SHARE. Basis и ValidFrom — для clinic_share_version:
// различающиеся основания через «; » и самое раннее начало периода принятых строк.
type ClinicShareFile struct {
	Check
	Shares    []ClinicShare
	Basis     string
	ValidFrom time.Time
}

const (
	columnClinic      = "clinic_code"
	columnClinicShare = "clinic_share"
	columnBasis       = "basis"
)

var clinicShareColumns = []string{columnDistrict, columnClinic, columnClinicShare, columnBasis,
	columnDataSource, columnStart, columnEnd}

// ParseClinicShares разбирает и проверяет доли район → клиника (FR046): район и клиника из справочника,
// пара без повторов, доля больше 0 и не больше 1, основание, источник и период. Затем сумма долей района
// сверяется с 1 ± 0,0001. Доли района принимаются целиком или никак (FR019: потребность района должна
// разойтись полностью): если отклонена любая строка района или сумма вне допуска, отклоняются все его строки.
func ParseClinicShares(data []byte, ref *Reference) ClinicShareFile {
	if ref == nil {
		return ClinicShareFile{Check: rejectFile(msgNoReference)}
	}
	rows, errs, fileErr := readCSV(data, clinicShareColumns)
	if fileErr != "" {
		return ClinicShareFile{Check: rejectFile(fileErr)}
	}
	type pair struct{ district, clinic int64 }
	var (
		sum      summary
		valid    []ClinicShare
		firstRow = map[pair]int{}
		units    = map[int64]int64{}
		// brokenRow — первая отклонённая строка распознанного района: такой район отклоняется целиком.
		brokenRow = map[int64]int{}
	)
	for _, row := range rows {
		if row.values == nil {
			continue
		}
		c := &rowCheck{row: row}
		s := ClinicShare{Row: row.line}

		if code, ok := c.required(columnDistrict); ok {
			if d, known := ref.District(code); known {
				s.DistrictID, s.DistrictCode = d.ID, d.Code
			} else {
				c.fail(columnDistrict, fmt.Sprintf("неизвестный район «%s»", shown(code)))
			}
		}
		if code, ok := c.required(columnClinic); ok {
			if cl, known := ref.Clinic(code); known {
				s.ClinicID, s.ClinicCode = cl.ID, cl.Code
			} else {
				c.fail(columnClinic, fmt.Sprintf("неизвестная клиника «%s»", shown(code)))
			}
		}
		if v, ok := c.share(columnClinicShare); ok {
			if u := v.TenThousandths(); u <= 0 || u > shareScale {
				c.fail(columnClinicShare, "доля должна быть больше 0 и не больше 1 (с точностью 4 знака)")
			} else {
				s.Share = v
			}
		}
		s.Basis = c.text(columnBasis)
		s.DataSource = c.text(columnDataSource)
		s.Period, _ = c.period()

		if s.DistrictID != 0 && s.ClinicID != 0 {
			key := pair{s.DistrictID, s.ClinicID}
			if first := firstRow[key]; first != 0 {
				c.fail(columnClinic, fmt.Sprintf("пара район %s — клиника %s уже указана в строке %d",
					s.DistrictCode, s.ClinicCode, first))
			} else if c.ok() {
				firstRow[key] = row.line
			}
		}
		if !c.ok() {
			errs = append(errs, c.errs...)
			if s.DistrictID != 0 && brokenRow[s.DistrictID] == 0 {
				brokenRow[s.DistrictID] = row.line
			}
			continue
		}
		units[s.DistrictID] += s.Share.TenThousandths()
		valid = append(valid, s)
	}

	var out ClinicShareFile
	var bases []string
	seenBasis := map[string]bool{}
	for _, s := range valid {
		if broken := brokenRow[s.DistrictID]; broken != 0 {
			errs = append(errs, RowError{Row: s.Row, Field: columnDistrict,
				Message: fmt.Sprintf("доли района %s отклонены целиком: в строке %d этого района ошибка", s.DistrictCode, broken)})
			continue
		}
		if !ShareSumWithinTolerance(units[s.DistrictID]) {
			errs = append(errs, RowError{Row: s.Row, Field: columnClinicShare,
				Message: fmt.Sprintf("сумма долей района %s по клиникам %s вне допуска 1 ± 0,0001",
					s.DistrictCode, formatShareUnits(units[s.DistrictID]))})
			continue
		}
		out.Shares = append(out.Shares, s)
		sum.add(s.DataSource, s.Period)
		if !seenBasis[s.Basis] {
			seenBasis[s.Basis] = true
			bases = append(bases, s.Basis)
		}
		if out.ValidFrom.IsZero() || s.Period.Start.Before(out.ValidFrom) {
			out.ValidFrom = s.Period.Start
		}
	}
	out.Basis = joinLimited(bases)
	finish(&out.Check, len(rows), len(out.Shares), errs, sum)
	return out
}
