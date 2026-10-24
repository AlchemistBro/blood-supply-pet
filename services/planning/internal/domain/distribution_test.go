package domain

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestSplitByWeights(t *testing.T) {
	cases := []struct {
		name    string
		total   int64
		weights []float64
		want    []int64
	}{
		{"поровну с остатком", 100, []float64{1, 1, 1}, []int64{34, 33, 33}},
		{"ненормированные веса, ничья по остатку - меньший индекс", 10, []float64{1, 3}, []int64{3, 7}},
		{"нулевой итог", 0, []float64{1, 2}, []int64{0, 0}},
		{"все веса нулевые", 50, []float64{0, 0}, []int64{0, 0}},
		{"отрицательный вес не получает долю", 9, []float64{-1, 1, 2}, []int64{0, 3, 6}},
		{"доли меньше единицы нормируются", 1000, []float64{0.3, 0.1}, []int64{750, 250}},
		{"пустые веса", 10, nil, []int64{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitByWeights(tc.total, tc.weights)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("splitByWeights(%d, %v) = %v, ожидали %v", tc.total, tc.weights, got, tc.want)
			}
			var sum int64
			for _, v := range got {
				sum += v
			}
			if sum > tc.total {
				t.Fatalf("сумма частей %d больше целого %d", sum, tc.total)
			}
		})
	}
}

var (
	lineA = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	lineB = uuid.MustParse("00000000-0000-0000-0000-00000000000b")
)

func distributionKinds() map[string]Kind {
	return map[string]Kind{
		"RBC":  {Code: "RBC", TypicalVolumeML: 300, GroupApplicable: true},
		"CRYO": {Code: "CRYO", TypicalVolumeML: 20, GroupApplicable: false},
	}
}

func TestDistributeForecast(t *testing.T) {
	groups := []GroupShare{{Group: "O+", Share: 0.5}, {Group: "A+", Share: 0.5}}
	shares := []ClinicShare{
		{DistrictID: 1, ClinicID: 10, Share: 0.6},
		{DistrictID: 1, ClinicID: 11, Share: 0.4},
		{DistrictID: 2, ClinicID: 20, Share: 0.5},
		{DistrictID: 2, ClinicID: 21, Share: 0.5},
	}
	cases := []struct {
		name     string
		forecast Forecast
		want     []EmergencyAllocation
	}{
		{
			name: "принимающая клиника из прогноза C02, объём округляется вниз",
			forecast: Forecast{Lines: []ForecastLine{
				{LineID: lineA, ClinicID: 10, Kind: "RBC", Group: "O+", VolumeML: 1000.7},
			}},
			want: []EmergencyAllocation{
				{LineID: lineA, ClinicID: 10, Kind: "RBC", Group: "O+", VolumeML: 1000, Source: SourceForecastC02},
			},
		},
		{
			name: "без принимающих клиник - по долям района, грубая оценка",
			forecast: Forecast{
				Territory: []DistrictWeight{{DistrictID: 1, Weight: 1}},
				Lines:     []ForecastLine{{LineID: lineA, Kind: "RBC", Group: "O+", VolumeML: 1000}},
			},
			want: []EmergencyAllocation{
				{LineID: lineA, ClinicID: 10, Kind: "RBC", Group: "O+", VolumeML: 600, Source: SourceDistrictShares, IsRoughEstimate: true},
				{LineID: lineA, ClinicID: 11, Kind: "RBC", Group: "O+", VolumeML: 400, Source: SourceDistrictShares, IsRoughEstimate: true},
			},
		},
		{
			name: "территория из двух районов с весами 1 и 3",
			forecast: Forecast{
				Territory: []DistrictWeight{{DistrictID: 2, Weight: 3}, {DistrictID: 1, Weight: 1}},
				Lines:     []ForecastLine{{LineID: lineA, Kind: "RBC", Group: "A+", VolumeML: 800}},
			},
			want: []EmergencyAllocation{
				{LineID: lineA, ClinicID: 10, Kind: "RBC", Group: "A+", VolumeML: 120, Source: SourceDistrictShares, IsRoughEstimate: true},
				{LineID: lineA, ClinicID: 11, Kind: "RBC", Group: "A+", VolumeML: 80, Source: SourceDistrictShares, IsRoughEstimate: true},
				{LineID: lineA, ClinicID: 20, Kind: "RBC", Group: "A+", VolumeML: 300, Source: SourceDistrictShares, IsRoughEstimate: true},
				{LineID: lineA, ClinicID: 21, Kind: "RBC", Group: "A+", VolumeML: 300, Source: SourceDistrictShares, IsRoughEstimate: true},
			},
		},
		{
			name: "группа не определена - по долям групп, ничья к меньшему коду",
			forecast: Forecast{Lines: []ForecastLine{
				{LineID: lineA, ClinicID: 10, Kind: "RBC", VolumeML: 101},
			}},
			want: []EmergencyAllocation{
				{LineID: lineA, ClinicID: 10, Kind: "RBC", Group: "A+", VolumeML: 51, Source: SourceForecastC02, GroupUndetermined: true},
				{LineID: lineA, ClinicID: 10, Kind: "RBC", Group: "O+", VolumeML: 50, Source: SourceForecastC02, GroupUndetermined: true},
			},
		},
		{
			name: "вид без группы - группа сбрасывается",
			forecast: Forecast{Lines: []ForecastLine{
				{LineID: lineB, ClinicID: 11, Kind: "CRYO", Group: "A+", VolumeML: 40},
				{LineID: lineA, ClinicID: 11, Kind: "CRYO", VolumeML: 60},
			}},
			want: []EmergencyAllocation{
				{LineID: lineA, ClinicID: 11, Kind: "CRYO", Group: "", VolumeML: 60, Source: SourceForecastC02},
				{LineID: lineB, ClinicID: 11, Kind: "CRYO", Group: "", VolumeML: 40, Source: SourceForecastC02},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := distributeForecast(&tc.forecast, shares, distributionKinds(), groups)
			if err != nil {
				t.Fatalf("ошибка: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("распределение:\n получили %+v\n ожидали  %+v", got, tc.want)
			}
			assertNotAboveForecast(t, &tc.forecast, got)
		})
	}
}

func TestDistributeForecastRejectsBadInput(t *testing.T) {
	groups := []GroupShare{{Group: "O+", Share: 1}}
	cases := []struct {
		name     string
		forecast Forecast
	}{
		{"грубая строка без территории", Forecast{Lines: []ForecastLine{{LineID: lineA, Kind: "RBC", Group: "O+", VolumeML: 10}}}},
		{"неизвестный вид", Forecast{Lines: []ForecastLine{{LineID: lineA, ClinicID: 1, Kind: "XXX", VolumeML: 10}}}},
		{"неизвестная группа", Forecast{Lines: []ForecastLine{{LineID: lineA, ClinicID: 1, Kind: "RBC", Group: "B-", VolumeML: 10}}}},
		{"отрицательный объём", Forecast{Lines: []ForecastLine{{LineID: lineA, ClinicID: 1, Kind: "RBC", Group: "O+", VolumeML: -1}}}},
		{"повтор строки", Forecast{Lines: []ForecastLine{
			{LineID: lineA, ClinicID: 1, Kind: "RBC", Group: "O+", VolumeML: 1},
			{LineID: lineA, ClinicID: 2, Kind: "RBC", Group: "O+", VolumeML: 1},
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := distributeForecast(&tc.forecast, nil, distributionKinds(), groups)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("ожидали ErrInvalidInput, получили %v", err)
			}
		})
	}
}

// Сценарная потребность не теряется молча: всё, что нельзя разложить, — ошибка входа.
func TestDistributeForecastNeverDropsDemandSilently(t *testing.T) {
	groups := []GroupShare{{Group: "O+", Share: 1}}
	shares := []ClinicShare{{DistrictID: 1, ClinicID: 10, Share: 1}, {DistrictID: 2, ClinicID: 20, Share: 0}}
	rough := func(v float64) []ForecastLine {
		return []ForecastLine{{LineID: lineA, Kind: "RBC", Group: "O+", VolumeML: v}}
	}
	cases := []struct {
		name     string
		forecast Forecast
		groups   []GroupShare
		shares   []ClinicShare
	}{
		{"район территории без положительных долей", Forecast{
			Territory: []DistrictWeight{{DistrictID: 1, Weight: 1}, {DistrictID: 2, Weight: 1}}, Lines: rough(100),
		}, groups, shares},
		{"район территории без долей вовсе", Forecast{
			Territory: []DistrictWeight{{DistrictID: 3, Weight: 1}}, Lines: rough(100),
		}, groups, shares},
		{"вид с группой без групп и без долей групп", Forecast{
			Lines: []ForecastLine{{LineID: lineA, ClinicID: 10, Kind: "RBC", VolumeML: 100}},
		}, nil, shares},
		{"вес района NaN", Forecast{
			Territory: []DistrictWeight{{DistrictID: 1, Weight: math.NaN()}}, Lines: rough(100),
		}, groups, shares},
		{"вес района бесконечный", Forecast{
			Territory: []DistrictWeight{{DistrictID: 1, Weight: math.Inf(1)}}, Lines: rough(100),
		}, groups, shares},
		{"доля клиники больше 1", Forecast{
			Territory: []DistrictWeight{{DistrictID: 1, Weight: 1}}, Lines: rough(100),
		}, groups, []ClinicShare{{DistrictID: 1, ClinicID: 10, Share: 1.5}}},
		{"объём строки бесконечный", Forecast{
			Lines: []ForecastLine{{LineID: lineA, ClinicID: 10, Kind: "RBC", Group: "O+", VolumeML: math.Inf(1)}},
		}, groups, shares},
		{"объём строки за пределом", Forecast{
			Lines: []ForecastLine{{LineID: lineA, ClinicID: 10, Kind: "RBC", Group: "O+", VolumeML: 2e9}},
		}, groups, shares},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := distributeForecast(&tc.forecast, tc.shares, distributionKinds(), tc.groups)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("ожидали ErrInvalidInput, получили %v", err)
			}
		})
	}
	// Район с нулевым весом не требует долей.
	ok := Forecast{Territory: []DistrictWeight{{DistrictID: 1, Weight: 1}, {DistrictID: 2, Weight: 0}}, Lines: rough(100)}
	got, err := distributeForecast(&ok, shares, distributionKinds(), groups)
	if err != nil || len(got) != 1 || got[0].ClinicID != 10 || got[0].VolumeML != 100 {
		t.Fatalf("район с нулевым весом: %+v, %v", got, err)
	}
}

func TestDistributeForecastWithoutForecast(t *testing.T) {
	got, err := distributeForecast(nil, nil, distributionKinds(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("без прогноза ожидали пустое распределение, получили %v, %v", got, err)
	}
}

// assertNotAboveForecast проверяет FR024: сумма распределённых величин по строке не превышает прогноз.
func assertNotAboveForecast(t *testing.T, f *Forecast, got []EmergencyAllocation) {
	t.Helper()
	perLine := map[uuid.UUID]int64{}
	for _, a := range got {
		perLine[a.LineID] += a.VolumeML
	}
	for _, l := range f.Lines {
		if float64(perLine[l.LineID]) > l.VolumeML {
			t.Fatalf("строка %s: распределено %d мл, в прогнозе %.2f", l.LineID, perLine[l.LineID], l.VolumeML)
		}
	}
}
