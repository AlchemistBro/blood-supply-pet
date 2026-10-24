package testdata_test

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
)

func units(t *testing.T, s string) int64 {
	t.Helper()
	whole, frac, _ := strings.Cut(s, ",")
	frac = (frac + "0000")[:4]
	w, err1 := strconv.ParseInt(whole, 10, 64)
	f, err2 := strconv.ParseInt(frac, 10, 64)
	if err1 != nil || err2 != nil {
		t.Fatalf("доля %q не разбирается", s)
	}
	return w*10000 + f
}

func TestCSVGeneratorsAreDeterministic(t *testing.T) {
	ref := refdata.MustLoad()
	cfg := testdata.DefaultCSVConfig()
	gens := map[string]func(*refdata.Set, testdata.CSVConfig) testdata.CSVFile{
		"profile": testdata.DistrictProfileCSV, "history": testdata.ConsumptionHistoryCSV, "shares": testdata.ClinicShareCSV,
	}
	for name, gen := range gens {
		a, b := gen(ref, cfg), gen(ref, cfg)
		if !bytes.Equal(a.Content, b.Content) || a.Name != b.Name {
			t.Fatalf("%s: файл недетерминирован", name)
		}
		if len(a.BadRows) != 0 {
			t.Fatalf("%s: в корректном файле битые строки %v", name, a.BadRows)
		}
		broken := cfg
		broken.Broken = true
		f := gen(ref, broken)
		if len(f.BadRows) == 0 || f.Rows != a.Rows+len(f.BadRows) {
			t.Fatalf("%s: битых строк %v, строк %d (корректных %d)", name, f.BadRows, f.Rows, a.Rows)
		}
		if f.BadRows[0] != a.Rows+2 {
			t.Fatalf("%s: битые строки начинаются с %d, ожидали %d", name, f.BadRows[0], a.Rows+2)
		}
	}
}

func TestDistrictProfileCSVSharesSumToOne(t *testing.T) {
	ref := refdata.MustLoad()
	f := testdata.DistrictProfileCSV(ref, testdata.DefaultCSVConfig())
	lines := strings.Split(strings.TrimSpace(string(f.Content)), "\n")
	if len(lines)-1 != len(ref.Districts) {
		t.Fatalf("строк %d, районов %d", len(lines)-1, len(ref.Districts))
	}
	for _, line := range lines[1:] {
		cells := strings.Split(line, ";")
		var sum int64
		for _, c := range cells[2 : 2+len(ref.BloodGroups)] {
			sum += units(t, c)
		}
		if sum != 10000 {
			t.Fatalf("сумма долей групп %d в строке %q", sum, line)
		}
	}
}

func TestClinicShareCSVSharesSumToOnePerDistrict(t *testing.T) {
	ref := refdata.MustLoad()
	f := testdata.ClinicShareCSV(ref, testdata.DefaultCSVConfig())
	sums := map[string]int64{}
	for _, line := range strings.Split(strings.TrimSpace(string(f.Content)), "\n")[1:] {
		cells := strings.Split(line, ";")
		sums[cells[0]] += units(t, cells[2])
	}
	if len(sums) != len(ref.Districts) {
		t.Fatalf("районов в файле %d, в справочнике %d", len(sums), len(ref.Districts))
	}
	for d, s := range sums {
		if s != 10000 {
			t.Fatalf("район %s: сумма долей %d", d, s)
		}
	}
}

func TestConsumptionHistoryCSVCoversYearByMonths(t *testing.T) {
	ref := refdata.MustLoad()
	f := testdata.ConsumptionHistoryCSV(ref, testdata.DefaultCSVConfig())
	active := 0
	for _, k := range ref.ComponentKinds {
		if k.IsActive {
			active++
		}
	}
	if f.Rows != active*12 {
		t.Fatalf("строк %d, ожидали %d", f.Rows, active*12)
	}
	if !strings.Contains(string(f.Content), "RBC;2025-02-01;2025-02-28;") {
		t.Fatal("нет февраля 2025 для RBC")
	}
}
