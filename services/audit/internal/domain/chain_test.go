package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func sample(i int) Record {
	return Record{
		ID: uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-00000000000" + string(rune('0'+i))), OccurredAt: time.Date(2026, 10, 20, 12, i, 0, 0, time.UTC),
		UserID: "operator-c001", Role: "CLINIC_OPERATOR", Module: "transfer", Action: "CONFIRM_TRANSFER",
		ObjectType: "obligation", ObjectID: "o-1", Result: "SUCCESS", Details: map[string]any{"qty": float64(i)},
		Producer: "transfer", SourceMessageID: uuid.MustParse("0192f3a1-7b2c-7d4e-8f90-0000000000a" + string(rune('0'+i))),
	}
}

func chain(t *testing.T, n int) []Record {
	t.Helper()
	seq, hash := int64(0), GenesisHash
	out := make([]Record, 0, n)
	for i := 0; i < n; i++ {
		r, err := Link(seq, hash, sample(i))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
		seq, hash = r.Seq, r.Hash
	}
	return out
}

func verify(records []Record) *BreakReport {
	v := NewVerifier()
	for _, r := range records {
		if br, err := v.Next(r); err != nil || br != nil {
			return br
		}
	}
	return nil
}

func TestIntactChainVerifies(t *testing.T) {
	if br := verify(chain(t, 5)); br != nil {
		t.Fatalf("целая цепочка признана порванной: %+v", br)
	}
}

func TestTamperedRecordIsDetected(t *testing.T) {
	records := chain(t, 5)
	records[2].Details["qty"] = float64(99)
	br := verify(records)
	if br == nil || br.Seq != 3 {
		t.Fatalf("подмена записи 3 не найдена: %+v", br)
	}
}

func TestDeletedRecordIsDetected(t *testing.T) {
	records := chain(t, 5)
	records = append(records[:1], records[2:]...)
	br := verify(records)
	if br == nil || br.Seq != 3 {
		t.Fatalf("удаление записи 2 не найдено: %+v", br)
	}
}

func TestHashIsDeterministic(t *testing.T) {
	a, _ := ComputeHash(GenesisHash, sample(1))
	b, _ := ComputeHash(GenesisHash, sample(1))
	if string(a) != string(b) {
		t.Fatal("хеш недетерминирован")
	}
}
