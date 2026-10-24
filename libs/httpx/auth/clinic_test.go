package auth

import (
	"encoding/json"
	"math"
	"testing"
)

func TestClinicIDMustBeAnExactPositiveInteger(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  int64
		ok    bool
	}{
		{"large JSON integer", json.RawMessage(`9007199254740993`), 9007199254740993, true},
		{"max JSON integer", json.RawMessage(`9223372036854775807`), math.MaxInt64, true},
		{"JSON fraction", json.RawMessage(`1.5`), 0, false},
		{"JSON overflow", json.RawMessage(`9223372036854775808`), 0, false},
		{"JSON array", json.RawMessage(`["42"]`), 42, true},
		{"numeric", float64(1), 1, true},
		{"string", " 42 ", 42, true},
		{"array", []any{"42"}, 42, true},
		{"fraction", 1.5, 0, false},
		{"small fraction", 0.5, 0, false},
		{"overflow", math.Exp2(63), 0, false},
		{"negative", float64(-1), 0, false},
		{"zero", float64(0), 0, false},
		{"nan", math.NaN(), 0, false},
		{"infinity", math.Inf(1), 0, false},
		{"missing", nil, 0, false},
		{"multiple", []any{"1", "2"}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseClinicID(tc.value)
			if ok != tc.ok || (ok && got != tc.want) {
				t.Fatalf("clinic_id: (%d, %v), want (%d, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}
