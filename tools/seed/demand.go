package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
)

func (s seeder) estimate(token string, profile, sharesFile uuid.UUID, year int) (uuid.UUID, error) {
	var versions struct {
		Items []struct {
			ID   uuid.UUID `json:"id"`
			File uuid.UUID `json:"statistics_file_id"`
		} `json:"items"`
	}
	if err := s.json(http.MethodGet, s.demand+"/shares/versions", token, nil, &versions); err != nil {
		return uuid.Nil, err
	}
	shares := uuid.Nil
	for _, version := range versions.Items {
		if version.File == sharesFile {
			shares = version.ID
			break
		}
	}
	if shares == uuid.Nil {
		return uuid.Nil, fmt.Errorf("не найдена версия долей")
	}
	var estimates []struct {
		ID      uuid.UUID `json:"demand_estimate_id"`
		Profile uuid.UUID `json:"profile_file_id"`
		Shares  uuid.UUID `json:"clinic_share_version_id"`
		Status  string    `json:"status"`
		Start   string    `json:"period_start"`
		End     string    `json:"period_end"`
	}
	if err := s.json(http.MethodGet, s.demand+"/estimates", token, nil, &estimates); err != nil {
		return uuid.Nil, err
	}
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
	end := time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
	for _, estimate := range estimates {
		if estimate.Profile == profile && estimate.Shares == shares && estimate.Status == "PUBLISHED" && estimate.Start == start && estimate.End == end {
			return estimate.ID, nil
		}
	}
	var history struct {
		Items []struct {
			ID     uuid.UUID `json:"id"`
			Source string    `json:"history_source"`
			Start  string    `json:"period_start"`
		} `json:"items"`
	}
	if err := s.json(http.MethodGet, s.demand+"/history", token, nil, &history); err != nil {
		return uuid.Nil, err
	}
	ids := make([]uuid.UUID, 0)
	for _, row := range history.Items {
		if row.Source == "FILE" && len(row.Start) >= 4 && row.Start[:4] == fmt.Sprint(year-1) {
			ids = append(ids, row.ID)
		}
	}
	if len(ids) == 0 {
		return uuid.Nil, fmt.Errorf("не найдена история прошлого года")
	}
	var created struct {
		ID uuid.UUID `json:"demand_estimate_id"`
	}
	if err := s.json(http.MethodPost, s.demand+"/estimates", token, map[string]any{"profile_file_id": profile, "clinic_share_version_id": shares, "history_ids": ids, "period_start": start, "period_end": end}, &created); err != nil {
		return uuid.Nil, fmt.Errorf("расчёт оценки: %w", err)
	}
	if err := s.json(http.MethodPost, s.demand+"/estimates/"+created.ID.String()+"/publish", token, struct{}{}, nil); err != nil {
		return uuid.Nil, fmt.Errorf("публикация оценки: %w", err)
	}
	return created.ID, nil
}
