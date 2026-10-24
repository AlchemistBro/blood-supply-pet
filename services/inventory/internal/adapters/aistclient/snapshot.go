package aistclient

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/services/inventory/internal/app"
)

var snapshotHeader = []string{"aist_unit_code", "clinic_code", "component_kind", "blood_group", "phenotype", "volume_ml", "expires_at", "storage_location", "status", "withdrawal_reason", "version", "changed_at"}

func (c *Client) Snapshot(ctx context.Context) ([]app.AistUnit, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/snapshot.csv", nil)
	if err != nil {
		return nil, &app.AistFailure{Operation: "snapshot", Kind: app.AistUnavailable}
	}
	resp, err := c.changes.Do(req)
	if err != nil {
		return nil, &app.AistFailure{Operation: "snapshot", Kind: classify(ctx, err)}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return nil, &app.AistFailure{Operation: "snapshot", Kind: app.AistHTTPStatus, HTTPStatus: resp.StatusCode}
	}
	const limit = 32 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(raw) > limit {
		return nil, &app.AistFailure{Operation: "snapshot", Kind: app.AistBadResponse}
	}
	return parseSnapshot(raw)
}

func parseSnapshot(raw []byte) ([]app.AistUnit, error) {
	r := csv.NewReader(bytes.NewReader(raw))
	header, err := r.Read()
	if err != nil || !slices.Equal(header, snapshotHeader) {
		return nil, &app.AistFailure{Operation: "snapshot", Kind: app.AistBadResponse}
	}
	seen := map[string]bool{}
	var out []app.AistUnit
	for row := 2; ; row++ {
		fields, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(out) >= 100000 {
			return nil, fmt.Errorf("снимок: строка %d не разобрана", row)
		}
		volume, e1 := strconv.ParseInt(fields[5], 10, 64)
		expires, e2 := time.Parse(time.RFC3339, fields[6])
		version, e3 := strconv.ParseInt(fields[10], 10, 64)
		changed, e4 := time.Parse(time.RFC3339Nano, fields[11])
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || seen[fields[0]] {
			return nil, fmt.Errorf("снимок: строка %d некорректна или повторяется", row)
		}
		seen[fields[0]] = true
		out = append(out, app.AistUnit{AistUnitCode: fields[0], ClinicCode: fields[1], ComponentKind: fields[2], BloodGroup: fields[3], Phenotype: fields[4], VolumeML: volume, ExpiresAt: expires, StorageLocation: fields[7], Status: fields[8], WithdrawalReason: fields[9], Version: version, ChangedAt: changed})
	}
	return out, nil
}
