package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlchemistBro/blood-supply-pet/libs/k1seal"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/domain"
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ app.Store = (*Store)(nil)

func (s *Store) InsertRequest(ctx context.Context, tx pgx.Tx, n app.RequestRecord, patient k1seal.Sealed) error {
	req, err := json.Marshal(n.Request)
	if err != nil {
		return err
	}
	result, err := json.Marshal(n.Result)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO clinical_request(clinical_request_id,clinic_id,created_by,request_status_code,payload,match_result,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$7)`, n.Request.RequestID, n.Request.ClinicID, n.CreatedBy, n.Status, req, result, n.Request.CreatedAt)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO clinical_request_patient VALUES($1,$2)`, n.Request.RequestID, string(patient)); err != nil {
		return err
	}
	for _, c := range n.Request.Constraints {
		if _, err := tx.Exec(ctx, `INSERT INTO request_constraint VALUES($1,$2,$3)`, n.Request.RequestID, c.Code, c.Value); err != nil {
			return err
		}
	}
	for _, v := range n.Result.Variants {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		id := app.VariantID(n.Request.RequestID, v.Rank)
		if _, err := tx.Exec(ctx, `INSERT INTO variant_offer VALUES($1,$2,$3,$4)`, id, n.Request.RequestID, v.Rank, raw); err != nil {
			return err
		}
		for _, unit := range v.UnitIDs {
			if _, err := tx.Exec(ctx, `INSERT INTO variant_offer_unit_link VALUES($1,$2)`, id, unit); err != nil {
				return err
			}
		}
	}
	return nil
}

func decodeRecord(row pgx.Row) (app.RequestRecord, error) {
	var n app.RequestRecord
	var request, result []byte
	if err := row.Scan(&request, &result, &n.CreatedBy, &n.Status, &n.Version); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return n, app.ErrNotFound
		}
		return n, err
	}
	if err := json.Unmarshal(request, &n.Request); err != nil {
		return n, err
	}
	if err := json.Unmarshal(result, &n.Result); err != nil {
		return n, err
	}
	return n, nil
}
func (s *Store) Request(ctx context.Context, tx pgx.Tx, id uuid.UUID, lock bool) (app.RequestRecord, error) {
	query := `SELECT payload,match_result,created_by,request_status_code,version FROM clinical_request WHERE clinical_request_id=$1`
	if lock {
		query += " FOR UPDATE"
	}
	var n app.RequestRecord
	var err error
	if tx != nil {
		n, err = decodeRecord(tx.QueryRow(ctx, query, id))
	} else {
		n, err = decodeRecord(s.pool.QueryRow(ctx, query, id))
	}
	if err != nil {
		return n, err
	}
	var selection app.Selection
	query = `SELECT variant_offer_id,choice_id,selected_by,selected_at FROM variant_selection WHERE clinical_request_id=$1`
	if tx != nil {
		err = tx.QueryRow(ctx, query, id).Scan(&selection.VariantID, &selection.ChoiceID, &selection.By, &selection.At)
	} else {
		err = s.pool.QueryRow(ctx, query, id).Scan(&selection.VariantID, &selection.ChoiceID, &selection.By, &selection.At)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return n, nil
	}
	if err != nil {
		return n, err
	}
	n.Selection = &selection
	return n, nil
}
func (s *Store) Requests(ctx context.Context, clinic int64, summary bool) ([]app.RequestRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT payload,CASE WHEN $2::boolean THEN match_summary ELSE match_result END,created_by,request_status_code,version FROM clinical_request WHERE ($1::bigint=0 OR clinic_id=$1) ORDER BY created_at DESC,clinical_request_id DESC LIMIT 100`, clinic, summary)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []app.RequestRecord{}
	for rows.Next() {
		n, err := decodeRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
func (s *Store) Patient(ctx context.Context, tx pgx.Tx, id uuid.UUID) (k1seal.Sealed, error) {
	var raw string
	if err := tx.QueryRow(ctx, `SELECT patient_case_sealed FROM clinical_request_patient WHERE clinical_request_id=$1`, id).Scan(&raw); err != nil {
		return "", err
	}
	return k1seal.ParseSealed(raw)
}
func (s *Store) SaveSelection(ctx context.Context, tx pgx.Tx, id uuid.UUID, c app.Selection, status string, at time.Time) error {
	if _, err := tx.Exec(ctx, `INSERT INTO variant_selection VALUES($1,$2,$3,$4,$5)`, id, c.VariantID, c.ChoiceID, c.By, c.At); err != nil {
		return err
	}
	return s.SetStatus(ctx, tx, id, status, at)
}
func (s *Store) SetStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, at time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE clinical_request SET request_status_code=$2,updated_at=$3,version=version+1 WHERE clinical_request_id=$1 AND request_status_code<>'CANCELLED'`, id, status, at)
	return err
}
func (s *Store) Rules(ctx context.Context, tx pgx.Tx) ([]domain.Rule, error) {
	var rows pgx.Rows
	var err error
	query := `SELECT payload FROM matching_rule ORDER BY rule_code,version_no`
	if tx != nil {
		rows, err = tx.Query(ctx, query)
	} else {
		rows, err = s.pool.Query(ctx, query)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Rule{}
	for rows.Next() {
		var raw []byte
		var r domain.Rule
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) InsertRule(ctx context.Context, tx pgx.Tx, r domain.Rule) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var until *time.Time
	if !r.ValidTo.IsZero() {
		until = &r.ValidTo
	}
	_, err = tx.Exec(ctx, `INSERT INTO matching_rule VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(rule_code,version_no) DO NOTHING`, r.RuleID, r.Code, r.Version, r.ValidFrom, until, raw)
	if err != nil {
		return fmt.Errorf("сохранение версии правила: %w", err)
	}
	return nil
}

func (s *Store) CloseRule(ctx context.Context, tx pgx.Tx, r domain.Rule, until time.Time) error {
	r.ValidTo = until
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE matching_rule SET valid_to=$2,payload=$3 WHERE matching_rule_id=$1 AND valid_to IS NULL`, r.RuleID, until, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return app.ErrConflict
	}
	return nil
}
