package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// t0 — время расчёта во всех примерах.
var t0 = time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)

var testPlanID = uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-000000000001")

func testKinds() []Kind {
	return []Kind{
		{Code: "RBC", TypicalVolumeML: 300, GroupApplicable: true},
		{Code: "CRYO", TypicalVolumeML: 20, GroupApplicable: false},
	}
}

func testGroups() []GroupShare {
	return []GroupShare{{Group: "O+", Share: 0.6}, {Group: "A+", Share: 0.4}}
}

// unitN строит детерминированный идентификатор единицы по номеру.
func unitN(n int) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte{byte(n >> 8), byte(n)})
}

// stock возвращает count единиц клиники одного вида и группы со сроком годности через days суток.
func stock(from, count int, clinic int64, kind, group string, volume int64, days int, status string) []Unit {
	out := make([]Unit, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, Unit{
			UnitID: unitN(from + i), ClinicID: clinic, Kind: kind, Group: group, VolumeML: volume,
			ExpiresAt: t0.Add(time.Duration(days)*24*time.Hour + time.Duration(i)*time.Hour), Status: status,
		})
	}
	return out
}

func baseInput() Input {
	return Input{PlanID: testPlanID, Kinds: testKinds(), GroupShares: testGroups(), Params: DefaultParams()}
}

func findCell(t *testing.T, st *state, clinic int64, kind, group string) *cell {
	t.Helper()
	c, ok := st.cells[cellKey{clinic: clinic, kind: kind, group: group}]
	if !ok {
		t.Fatalf("нет ячейки %d/%s/%s", clinic, kind, group)
	}
	return c
}

func TestTargetStockIsNormPlusBackgroundPlusEmergency(t *testing.T) {
	cases := []struct {
		name                         string
		in                           func() Input
		clinic                       int64
		kind, group                  string
		norm, bg, em, target, rough  int64
		wantStock, wantInbound, need int64
	}{
		{
			name: "вид без группы: норматив вверх, фон по правилу округления, прогноз вниз",
			in: func() Input {
				in := baseInput()
				in.Norms = []Norm{{ClinicID: 1, Kind: "CRYO", NormML: 1000.2}}
				in.Background = []Background{{ClinicID: 1, Kind: "CRYO", Group: "A+", VolumeML: 500.5}}
				in.Forecast = &Forecast{Lines: []ForecastLine{{LineID: lineA, ClinicID: 1, Kind: "CRYO", VolumeML: 300.9}}}
				return in
			},
			clinic: 1, kind: "CRYO", group: "",
			norm: 1001, bg: 501, em: 300, target: 1802, need: 1802,
		},
		{
			name: "вид с группой: норматив делится по долям групп",
			in: func() Input {
				in := baseInput()
				in.Norms = []Norm{{ClinicID: 1, Kind: "RBC", NormML: 1000}}
				return in
			},
			clinic: 1, kind: "RBC", group: "O+",
			norm: 600, target: 600, need: 600,
		},
		{
			name: "фон без группы делится по долям групп, с группой - целиком",
			in: func() Input {
				in := baseInput()
				in.Background = []Background{
					{ClinicID: 1, Kind: "RBC", VolumeML: 1000},
					{ClinicID: 1, Kind: "RBC", Group: "A+", VolumeML: 100},
				}
				return in
			},
			clinic: 1, kind: "RBC", group: "A+",
			bg: 500, target: 500, need: 500,
		},
		{
			name: "запас и входящие подтверждённые обязательства уменьшают потребность",
			in: func() Input {
				in := baseInput()
				in.Norms = []Norm{{ClinicID: 1, Kind: "RBC", NormML: 5000}}
				in.Units = stock(1, 4, 1, "RBC", "O+", 300, 10, UnitStatusInStock)
				in.Units = append(in.Units, stock(10, 2, 1, "RBC", "O+", 300, 10, UnitStatusQuarantine)...)
				in.Units = append(in.Units, stock(20, 1, 1, "RBC", "O+", 300, -1, UnitStatusInStock)...)
				in.Obligations = []Obligation{
					{ObligationID: unitN(100), SourceClinicID: 2, TargetClinicID: 1, Kind: "RBC", Group: "O+", VolumeML: 600, Status: ObligationConfirmed},
					{ObligationID: unitN(101), SourceClinicID: 2, TargetClinicID: 1, Kind: "RBC", Group: "O+", VolumeML: 900, Status: ObligationPending},
					{ObligationID: unitN(102), SourceClinicID: 1, TargetClinicID: 3, Kind: "RBC", Group: "O+", VolumeML: 300, Status: ObligationTransportRequested},
				}
				return in
			},
			clinic: 1, kind: "RBC", group: "O+",
			norm: 3000, target: 3000, wantStock: 900, wantInbound: 600, need: 1500,
		},
		{
			name: "прогноз по долям района помечает целевой запас грубой оценкой",
			in: func() Input {
				in := baseInput()
				in.ClinicShares = []ClinicShare{{DistrictID: 1, ClinicID: 1, Share: 1}}
				in.Forecast = &Forecast{
					Territory: []DistrictWeight{{DistrictID: 1, Weight: 1}},
					Lines:     []ForecastLine{{LineID: lineA, Kind: "RBC", Group: "O+", VolumeML: 900}},
				}
				return in
			},
			clinic: 1, kind: "RBC", group: "O+",
			em: 900, target: 900, rough: 1, need: 900,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := prepare(tc.in(), t0)
			if err != nil {
				t.Fatalf("ошибка: %v", err)
			}
			c := findCell(t, st, tc.clinic, tc.kind, tc.group)
			if c.normML != tc.norm || c.backgroundML != tc.bg || c.emergencyML != tc.em || c.targetML() != tc.target {
				t.Fatalf("норматив/фон/ЧС/цель = %d/%d/%d/%d, ожидали %d/%d/%d/%d",
					c.normML, c.backgroundML, c.emergencyML, c.targetML(), tc.norm, tc.bg, tc.em, tc.target)
			}
			if c.targetML() != c.normML+c.backgroundML+c.emergencyML {
				t.Fatalf("цель %d не равна норматив + фон + ЧС", c.targetML())
			}
			if c.rough != (tc.rough == 1) {
				t.Fatalf("грубая оценка = %v", c.rough)
			}
			if c.stockML() != tc.wantStock || c.inboundML != tc.wantInbound || c.needML() != tc.need {
				t.Fatalf("запас/входящие/потребность = %d/%d/%d, ожидали %d/%d/%d",
					c.stockML(), c.inboundML, c.needML(), tc.wantStock, tc.wantInbound, tc.need)
			}
		})
	}
}

func TestProtectedVolumeOfSource(t *testing.T) {
	in := baseInput()
	in.Norms = []Norm{{ClinicID: 5, Kind: "RBC", NormML: 3000}}
	in.Background = []Background{{ClinicID: 5, Kind: "RBC", Group: "O+", VolumeML: 1000}}
	in.Units = stock(1, 30, 5, "RBC", "O+", 300, 20, UnitStatusInStock)
	in.Obligations = []Obligation{
		{ObligationID: unitN(200), SourceClinicID: 5, TargetClinicID: 6, Kind: "RBC", Group: "O+", VolumeML: 600, Status: ObligationConfirmed},
		{ObligationID: unitN(201), SourceClinicID: 5, TargetClinicID: 6, Kind: "RBC", Group: "A+", VolumeML: 300, Status: ObligationTransportRequested},
		{ObligationID: unitN(202), SourceClinicID: 5, TargetClinicID: 7, Kind: "RBC", Group: "O+", VolumeML: 900, Status: ObligationPending},
		{ObligationID: unitN(203), SourceClinicID: 5, TargetClinicID: 7, Kind: "RBC", Group: "O+", VolumeML: 900, Status: "CANCELLED"},
		{ObligationID: unitN(204), SourceClinicID: 8, TargetClinicID: 5, Kind: "RBC", Group: "O+", VolumeML: 900, Status: ObligationConfirmed},
	}
	st, err := prepare(in, t0)
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	pv := st.sources[kindKey{clinic: 5, kind: "RBC"}]
	if pv == nil {
		t.Fatal("нет защищённого объёма клиники 5")
	}
	cases := []struct {
		name      string
		got, want int64
	}{
		{"норматив", pv.normML, 3000},
		{"фон за период", pv.backgroundML, 1000},
		{"подтверждённые исходящие обязательства (PENDING и чужие не в счёт)", pv.obligationsML, 900},
		{"защищённый объём = норматив + фон + обязательства", pv.protectedML(), 4900},
		{"защищённый объём в единицах, вверх по типовому объёму", pv.protectedUnits(), 17},
		{"подтверждённый запас", pv.availableML, 9000},
		{"подтверждённый запас в единицах", pv.availableUnits, 30},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: %d, ожидали %d", tc.name, tc.got, tc.want)
		}
	}
}

func TestPrepareRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(in *Input)
	}{
		{"нет идентификатора плана", func(in *Input) { in.PlanID = uuid.Nil }},
		{"повтор вида", func(in *Input) { in.Kinds = append(in.Kinds, Kind{Code: "RBC", TypicalVolumeML: 1}) }},
		{"вид без типового объёма", func(in *Input) { in.Kinds = append(in.Kinds, Kind{Code: "PLT"}) }},
		{"сумма долей групп не 1", func(in *Input) { in.GroupShares = []GroupShare{{Group: "O+", Share: 0.5}} }},
		{"повтор норматива", func(in *Input) {
			in.Norms = []Norm{{ClinicID: 1, Kind: "RBC", NormML: 1}, {ClinicID: 1, Kind: "RBC", NormML: 2}}
		}},
		{"норматив неизвестного вида", func(in *Input) { in.Norms = []Norm{{ClinicID: 1, Kind: "XXX", NormML: 1}} }},
		{"отрицательный фон", func(in *Input) { in.Background = []Background{{ClinicID: 1, Kind: "RBC", VolumeML: -5}} }},
		{"повтор единицы", func(in *Input) {
			in.Units = append(stock(1, 1, 1, "RBC", "O+", 300, 5, UnitStatusInStock), stock(1, 1, 2, "RBC", "O+", 300, 5, UnitStatusInStock)...)
		}},
		{"единица неизвестной группы", func(in *Input) { in.Units = stock(1, 1, 1, "RBC", "B-", 300, 5, UnitStatusInStock) }},
		{"единица без объёма", func(in *Input) { in.Units = stock(1, 1, 1, "RBC", "O+", 0, 5, UnitStatusInStock) }},
		{"обязательство на себя", func(in *Input) {
			in.Obligations = []Obligation{{ObligationID: unitN(1), SourceClinicID: 1, TargetClinicID: 1, Kind: "RBC", Group: "O+", VolumeML: 1, Status: ObligationConfirmed}}
		}},
		{"повтор маршрута доставки", func(in *Input) {
			in.Delivery = []DeliveryEstimate{{SourceClinicID: 1, TargetClinicID: 2, Minutes: 5}, {SourceClinicID: 1, TargetClinicID: 2, Minutes: 6}}
		}},
		{"отрицательное время доставки", func(in *Input) {
			in.Delivery = []DeliveryEstimate{{SourceClinicID: 1, TargetClinicID: 2, Minutes: -1}}
		}},
		{"порог покрытия вне (0, 1]", func(in *Input) { in.Params.CoverageThreshold = 1.5 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInput()
			tc.mutate(&in)
			if _, err := prepare(in, t0); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("ожидали ErrInvalidInput, получили %v", err)
			}
		})
	}
}
