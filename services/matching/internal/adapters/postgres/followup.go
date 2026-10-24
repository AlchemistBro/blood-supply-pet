package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlchemistBro/blood-supply-pet/services/matching/internal/app"
)

func (s *Store) ClaimFollowUp(ctx context.Context, tx pgx.Tx, id uuid.UUID, version int64, eventType string) (bool, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO matching_followup VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, version, eventType)
	return tag.RowsAffected() > 0, err
}

func (s *Store) InsertOutgoingChoice(ctx context.Context, tx pgx.Tx, request uuid.UUID, c app.OutgoingChoice) error {
	_, err := tx.Exec(ctx, `INSERT INTO matching_outgoing_choice VALUES($1,$2,$3,$4)`, c.ChoiceID, request, c.Source, c.Quantity)
	return err
}
func (s *Store) OutgoingChoices(ctx context.Context, tx pgx.Tx, request uuid.UUID) ([]app.OutgoingChoice, error) {
	rows, err := tx.Query(ctx, `SELECT choice_id,source_clinic_id,requested_qty FROM matching_outgoing_choice WHERE clinical_request_id=$1 ORDER BY source_clinic_id`, request)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []app.OutgoingChoice{}
	for rows.Next() {
		var c app.OutgoingChoice
		if err := rows.Scan(&c.ChoiceID, &c.Source, &c.Quantity); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
