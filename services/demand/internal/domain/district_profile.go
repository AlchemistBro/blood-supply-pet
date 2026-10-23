package domain

import (
	"fmt"
)

// DistrictProfile — O04 территориальный профиль района: население и доли групп AB0/Rh s(r, g).
// Используется только в фоновой оценке (FR018): заявка и группа пострадавшего из него не создаются.
type DistrictProfile struct {
	Row          int
	DistrictID   int64
	DistrictCode string
	Population   int64
	GroupShares  map[string]Share
	DataSource   string
	Period       Period
}

// DistrictProfileFile — итог разбора файла DISTRICT_PROFILE.
type DistrictProfileFile struct {
	Check
	Profiles []DistrictProfile
}

const (
	columnDistrict   = "district_code"
	columnPopulation = "population"
	groupSharePrefix = "share_"
	// fieldGroupShares — поле ошибки суммы долей групп: относится ко всем колонкам share_*.
	fieldGroupShares = "share_*"
)

func profileColumns(ref *Reference) []string {
	cols := []string{columnDistrict, columnPopulation}
	for _, g := range ref.groups {
		cols = append(cols, groupSharePrefix+g)
	}
	return append(cols, columnDataSource, columnStart, columnEnd)
}

// ParseDistrictProfiles разбирает и проверяет файл профиля районов (FR016, FR017): район из справочника и
// без повторов, население, доли всех групп от 0 до 1 с суммой 1 ± 0,0001, источник и период.
func ParseDistrictProfiles(data []byte, ref *Reference) DistrictProfileFile {
	if ref == nil {
		return DistrictProfileFile{Check: rejectFile(msgNoReference)}
	}
	rows, errs, fileErr := readCSV(data, profileColumns(ref))
	if fileErr != "" {
		return DistrictProfileFile{Check: rejectFile(fileErr)}
	}
	var out DistrictProfileFile
	var sum summary
	firstRow := map[int64]int{}
	for _, row := range rows {
		if row.values == nil {
			continue
		}
		c := &rowCheck{row: row}
		p := DistrictProfile{Row: row.line, GroupShares: make(map[string]Share, len(ref.groups))}

		if code, ok := c.required(columnDistrict); ok {
			d, known := ref.District(code)
			switch {
			case !known:
				c.fail(columnDistrict, fmt.Sprintf("неизвестный район «%s»", code))
			case firstRow[d.ID] != 0:
				c.fail(columnDistrict, fmt.Sprintf("район %s уже указан в строке %d", code, firstRow[d.ID]))
			default:
				p.DistrictID, p.DistrictCode = d.ID, d.Code
			}
		}
		if n, ok := c.intInRange(columnPopulation, 1, maxPopulation, "население должно быть целым числом больше 0"); ok {
			p.Population = n
		}

		var shareSum int64
		sharesOK := true
		for _, g := range ref.groups {
			col := groupSharePrefix + g
			s, ok := c.share(col)
			if !ok {
				sharesOK = false
				continue
			}
			if s.TenThousandths() > shareScale {
				c.fail(col, "доля должна быть от 0 до 1")
				sharesOK = false
				continue
			}
			p.GroupShares[g] = s
			shareSum += s.TenThousandths()
		}
		// FR016: сумма долей AB0/Rh района — 100 % с допуском; проверяется, только если все доли разобраны.
		if sharesOK && !ShareSumWithinTolerance(shareSum) {
			c.fail(fieldGroupShares, fmt.Sprintf("сумма долей групп AB0/Rh %s вне допуска 1 ± 0,0001",
				formatShareUnits(shareSum)))
		}

		p.DataSource = c.text(columnDataSource)
		p.Period, _ = c.period()
		sum.add(p.DataSource, p.Period)

		if !c.ok() {
			errs = append(errs, c.errs...)
			continue
		}
		firstRow[p.DistrictID] = row.line
		out.Profiles = append(out.Profiles, p)
	}
	finish(&out.Check, len(rows), len(out.Profiles), errs, sum)
	return out
}
