package aistclient

import (
	"strings"
	"testing"
)

func TestSnapshotRejectsDuplicateOrMalformedRowWithoutCodes(t *testing.T) {
	header := strings.Join(snapshotHeader, ",") + "\n"
	row := "SECRET-U,C001,RBC,O+,,300,2026-12-01T00:00:00Z,storage,IN_STOCK,,1,2026-10-24T00:00:00Z\n"
	items, err := parseSnapshot([]byte(header + row))
	if err != nil || len(items) != 1 {
		t.Fatalf("снимок: %v", err)
	}
	for _, raw := range []string{header + row + row, header + `"SECRET-U`, header + strings.Replace(row, ",300,", ",invalid,", 1)} {
		_, err := parseSnapshot([]byte(raw))
		if err == nil || strings.Contains(err.Error(), "SECRET-U") {
			t.Fatalf("неверный снимок: %v", err)
		}
	}
}
