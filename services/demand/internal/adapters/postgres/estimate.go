package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/demand"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/demand/internal/domain"
)

func (s *Store) LoadEstimateInput(ctx context.Context, tx pgx.Tx, in app.EstimateSelection) (domain.EstimateInput, error) {
	out := domain.EstimateInput{Period: in.Period}
	rows, err := tx.Query(ctx, `SELECT p.district_id,p.population,g.blood_group_code,g.group_share::float8 FROM district_profile p JOIN district_group_share g USING(district_profile_id) JOIN statistics_file f USING(statistics_file_id) WHERE p.statistics_file_id=$1 AND f.file_kind='DISTRICT_PROFILE' AND f.check_status<>'REJECTED' ORDER BY p.district_id,g.blood_group_code`, in.ProfileFileID)
	if err != nil {
		return out, err
	}
	index := map[int64]int{}
	for rows.Next() {
		var district, population int64
		var group string
		var share float64
		if err := rows.Scan(&district, &population, &group, &share); err != nil {
			rows.Close()
			return out, err
		}
		i, ok := index[district]
		if !ok {
			i = len(out.Profiles)
			index[district] = i
			out.Profiles = append(out.Profiles, domain.DistrictProfile{DistrictID: district, Population: population, GroupShares: map[string]domain.Share{}, Period: in.Period})
		}
		out.Profiles[i].GroupShares[group] = domain.Share(share)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, `SELECT component_kind_code,period_start,period_end,consumed_volume_ml,history_source FROM consumption_history WHERE consumption_history_id=ANY($1::uuid[]) ORDER BY component_kind_code,period_start`, in.HistoryIDs)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var h domain.ConsumptionRecord
		if err := rows.Scan(&h.KindCode, &h.Period.Start, &h.Period.End, &h.ConsumedVolumeML, &h.Source); err != nil {
			rows.Close()
			return out, err
		}
		out.History = append(out.History, h)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.History) != len(in.HistoryIDs) {
		return out, fmt.Errorf("%w: выбранная история отсутствует", domain.ErrEstimateInput)
	}
	rows, err = tx.Query(ctx, `SELECT s.district_id,s.clinic_id,s.clinic_share::float8 FROM clinic_share s JOIN clinic_share_version v USING(clinic_share_version_id) WHERE s.clinic_share_version_id=$1 AND v.valid_from<=$2 ORDER BY s.district_id,s.clinic_id`, in.ShareVersionID, in.Period.Start)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var h domain.ClinicShare
		var share float64
		if err := rows.Scan(&h.DistrictID, &h.ClinicID, &share); err != nil {
			return out, err
		}
		h.Share = domain.Share(share)
		out.Shares = append(out.Shares, h)
	}
	return out, rows.Err()
}

func (s *Store) SaveEstimate(ctx context.Context, tx pgx.Tx, n *app.EstimateRecord, result domain.Estimate) error {
	if err := tx.QueryRow(ctx, `SELECT nextval('demand_estimate_version_seq')`).Scan(&n.Version); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO demand_estimate(demand_estimate_id,version_no,period_start,period_end,profile_file_id,clinic_share_version_id,estimate_status_code,calculated_at) VALUES($1,$2,$3,$4,$5,$6,'CALCULATED',$7)`, n.ID, n.Version, n.Selection.Period.Start, n.Selection.Period.End, n.Selection.ProfileFileID, n.Selection.ShareVersionID, n.At); err != nil {
		return err
	}
	for _, id := range n.Selection.HistoryIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO estimate_history_link VALUES($1,$2)`, n.ID, id); err != nil {
			return err
		}
	}
	for _, line := range result.Clinics {
		if _, err := tx.Exec(ctx, `INSERT INTO clinic_demand(demand_estimate_id,clinic_id,component_kind_code,blood_group_code,demand_volume_ml) VALUES($1,$2,$3,NULLIF($4,''),$5)`, n.ID, line.ClinicID, line.KindCode, line.BloodGroup, line.VolumeML); err != nil {
			return err
		}
	}
	return nil
}

type estimateDB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Store) SharesEvent(ctx context.Context, tx pgx.Tx, fileID uuid.UUID) (demand.ClinicSharesPublished, error) {
	var event demand.ClinicSharesPublished
	if err := tx.QueryRow(ctx, `SELECT clinic_share_version_id,version_no FROM clinic_share_version WHERE statistics_file_id=$1`, fileID).Scan(&event.ClinicShareVersionID, &event.VersionNo); err != nil {
		return event, err
	}
	rows, err := tx.Query(ctx, `SELECT district_id,clinic_id,clinic_share::float8 FROM clinic_share WHERE clinic_share_version_id=$1 ORDER BY district_id,clinic_id`, event.ClinicShareVersionID)
	if err != nil {
		return event, err
	}
	defer rows.Close()
	for rows.Next() {
		var line demand.ClinicShare
		if err := rows.Scan(&line.DistrictID, &line.ClinicID, &line.Share); err != nil {
			return event, err
		}
		event.Shares = append(event.Shares, line)
	}
	return event, rows.Err()
}

func (s *Store) ReadEstimate(ctx context.Context, tx pgx.Tx, id uuid.UUID, lock bool) (app.EstimateRecord, error) {
	var db estimateDB = s.pool
	if tx != nil {
		db = tx
	}
	query := `SELECT demand_estimate_id,version_no,period_start,period_end,profile_file_id,clinic_share_version_id,estimate_status_code,calculated_at,COALESCE(confirmed_by,''),rejection_reason FROM demand_estimate WHERE demand_estimate_id=$1`
	if lock {
		query += " FOR UPDATE"
	}
	var n app.EstimateRecord
	err := db.QueryRow(ctx, query, id).Scan(&n.ID, &n.Version, &n.Selection.Period.Start, &n.Selection.Period.End, &n.Selection.ProfileFileID, &n.Selection.ShareVersionID, &n.Status, &n.At, &n.ConfirmedBy, &n.RejectionReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, app.ErrNotFound
	}
	if err != nil {
		return n, err
	}
	rows, err := db.Query(ctx, `SELECT clinic_id,component_kind_code,COALESCE(blood_group_code,''),demand_volume_ml::float8 FROM clinic_demand WHERE demand_estimate_id=$1 ORDER BY clinic_id,component_kind_code,blood_group_code`, id)
	if err != nil {
		return n, err
	}
	n.Lines = make([]demand.DemandLine, 0)
	for rows.Next() {
		var line demand.DemandLine
		if err := rows.Scan(&line.ClinicID, &line.ComponentKind, &line.BloodGroup, &line.DemandVolumeML); err != nil {
			rows.Close()
			return n, err
		}
		n.Lines = append(n.Lines, line)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return n, err
	}
	rows, err = db.Query(ctx, `SELECT consumption_history_id FROM estimate_history_link WHERE demand_estimate_id=$1 ORDER BY consumption_history_id`, id)
	if err != nil {
		return n, err
	}
	defer rows.Close()
	for rows.Next() {
		var historyID uuid.UUID
		if err := rows.Scan(&historyID); err != nil {
			return n, err
		}
		n.Selection.HistoryIDs = append(n.Selection.HistoryIDs, historyID)
	}
	return n, rows.Err()
}

func (s *Store) ListEstimates(ctx context.Context) ([]app.EstimateRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT demand_estimate_id FROM demand_estimate ORDER BY version_no DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]app.EstimateRecord, 0, len(ids))
	for _, id := range ids {
		n, err := s.ReadEstimate(ctx, nil, id, false)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func (s *Store) MarkEstimatePublished(ctx context.Context, tx pgx.Tx, id uuid.UUID, actor string, at time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE demand_estimate SET estimate_status_code='PUBLISHED',confirmed_by=$2,confirmed_at=$3 WHERE demand_estimate_id=$1 AND estimate_status_code='CALCULATED'`, id, actor, at)
	return err
}

func (s *Store) MarkEstimateRejected(ctx context.Context, tx pgx.Tx, id uuid.UUID, actor, reason string, at time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE demand_estimate SET estimate_status_code='REJECTED',rejection_reason=$2,rejected_by=$3,rejected_at=$4 WHERE demand_estimate_id=$1 AND estimate_status_code='CALCULATED'`, id, reason, actor, at)
	return err
}
