package domain

import (
	"testing"
)

func TestParseShare_RoundsToFourDigitsHalfAwayFromZero(t *testing.T) {
	cases := []struct {
		in   string
		want int64 // десятитысячные
	}{
		{"0", 0},
		{"1", 10000},
		{"0.35", 3500},
		{"0,35", 3500},
		{"0,3333", 3333},
		{"0.12344", 1234},
		{"0.12345", 1235},     // половина — от нуля
		{"0.123449999", 1234}, // решает только пятый знак и дальше, без двоичной погрешности
		{"0.99995", 10000},
		{"0.00004", 0},
		{"0.00005", 1},
		{"1.00004", 10000},
		{" 0.5 ", 5000},
		{"1.2", 12000},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			s, err := ParseShare(c.in)
			if err != nil {
				t.Fatalf("ParseShare(%q): %v", c.in, err)
			}
			if got := s.TenThousandths(); got != c.want {
				t.Fatalf("ParseShare(%q) = %d десятитысячных, ожидали %d", c.in, got, c.want)
			}
		})
	}
}

func TestParseShare_RejectsNonDecimal(t *testing.T) {
	for _, in := range []string{"", "-0.1", "abc", "NaN", "Inf", "1e-3", "0..1", ".5", "1/3", "0,1,2", "1234567"} {
		t.Run(in, func(t *testing.T) {
			if _, err := ParseShare(in); err == nil {
				t.Fatalf("ParseShare(%q) должна вернуть ошибку", in)
			}
		})
	}
}

func TestShareSumWithinTolerance_Boundaries(t *testing.T) {
	cases := []struct {
		sum  int64
		want bool
	}{
		{10000, true},
		{10001, true},  // 1,0001
		{9999, true},   // 0,9999
		{10002, false}, // 1,0002
		{9998, false},  // 0,9998
		{12000, false}, // 1,2
		{0, false},
	}
	for _, c := range cases {
		if got := ShareSumWithinTolerance(c.sum); got != c.want {
			t.Errorf("ShareSumWithinTolerance(%d) = %v, ожидали %v", c.sum, got, c.want)
		}
	}
}

func TestFormatShareUnits_RussianDecimalComma(t *testing.T) {
	cases := map[int64]string{12000: "1,2000", 10000: "1,0000", 9998: "0,9998", 5: "0,0005"}
	for in, want := range cases {
		if got := formatShareUnits(in); got != want {
			t.Errorf("formatShareUnits(%d) = %q, ожидали %q", in, got, want)
		}
	}
}

func TestNewPeriod(t *testing.T) {
	p, err := NewPeriod(date(2026, 1, 1), date(2026, 1, 31))
	if err != nil {
		t.Fatal(err)
	}
	if p.Days() != 31 {
		t.Fatalf("дней %d, ожидали 31", p.Days())
	}
	one := period(t, date(2026, 2, 1), date(2026, 2, 1))
	if one.Days() != 1 {
		t.Fatalf("однодневный период: %d дней", one.Days())
	}
	if _, err := NewPeriod(date(2026, 2, 1), date(2026, 1, 31)); err == nil {
		t.Fatal("конец раньше начала должен давать ошибку")
	}
	if !p.Overlaps(period(t, date(2026, 1, 31), date(2026, 2, 5))) {
		t.Fatal("периоды с общей границей пересекаются: границы включаются")
	}
	if p.Overlaps(one) {
		t.Fatal("январь и 1 февраля не пересекаются")
	}
}

func TestApportion(t *testing.T) {
	cases := []struct {
		name    string
		total   int64
		weights []int64
		want    []int64
	}{
		{"поровну без остатка", 300, []int64{1, 1, 1}, []int64{100, 100, 100}},
		{"равные остатки — по порядку", 10, []int64{1, 1, 1}, []int64{4, 3, 3}},
		{"половина мл — первому", 1, []int64{5000, 5000}, []int64{1, 0}},
		{"наибольший остаток", 10, []int64{3333, 3333, 3334}, []int64{3, 3, 4}},
		{"нулевой вес ничего не получает", 7, []int64{0, 1}, []int64{0, 7}},
		{"ноль делится в нули", 0, []int64{2, 3}, []int64{0, 0}},
		{"нормирование на сумму 1,0001", 10001, []int64{5000, 5001}, []int64{5000, 5001}},
		{"большие значения без переполнения", 999_999_999_999, []int64{2_147_483_647, 1}, []int64{999_999_999_533, 466}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := apportion(c.total, c.weights)
			if err != nil {
				t.Fatal(err)
			}
			var sum int64
			for i := range got {
				sum += got[i]
				if got[i] != c.want[i] {
					t.Fatalf("apportion(%d, %v) = %v, ожидали %v", c.total, c.weights, got, c.want)
				}
			}
			if sum != c.total {
				t.Fatalf("сумма частей %d ≠ %d", sum, c.total)
			}
		})
	}
}

func TestApportion_Errors(t *testing.T) {
	if _, err := apportion(10, []int64{0, 0}); err == nil {
		t.Fatal("нулевая сумма весов — ошибка")
	}
	if _, err := apportion(10, []int64{1, -1, 5}); err == nil {
		t.Fatal("отрицательный вес — ошибка")
	}
	if _, err := apportion(-1, []int64{1}); err == nil {
		t.Fatal("отрицательный объём — ошибка")
	}
	if _, err := apportion(10, nil); err == nil {
		t.Fatal("пустой набор весов — ошибка")
	}
}

func TestMulDivRoundHalfUp(t *testing.T) {
	cases := []struct{ a, b, c, want int64 }{
		{3, 1, 2, 2}, // 1,5 → 2
		{1, 1, 2, 1}, // 0,5 → 1
		{1, 1, 4, 0}, // 0,25 → 0
		{3, 1, 4, 1}, // 0,75 → 1
		{5, 2, 4, 3}, // 2,5 → 3
		{3_650_000, 31, 365, 310_000},
		{999_999_999_999, 366, 1, 365_999_999_999_634},
	}
	for _, c := range cases {
		got, err := mulDivRoundHalfUp(c.a, c.b, c.c)
		if err != nil {
			t.Fatalf("mulDivRoundHalfUp(%d,%d,%d): %v", c.a, c.b, c.c, err)
		}
		if got != c.want {
			t.Errorf("mulDivRoundHalfUp(%d,%d,%d) = %d, ожидали %d", c.a, c.b, c.c, got, c.want)
		}
	}
}
