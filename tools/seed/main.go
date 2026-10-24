// seed наполняет отдельный синтетический стенд через HTTP, без прямой записи в базы сервисов.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/int02"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
)

type seeder struct {
	ctx                                                   context.Context
	http                                                  *http.Client
	inventory, demand, planning, matching, keycloak, aist string
}

func main() {
	if err := run(); err != nil {
		slog.Error("Демо-наполнение не завершено", "err", err)
		os.Exit(1)
	}
}
func setting(key, def string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return def
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	s := seeder{ctx: ctx, http: &http.Client{Timeout: 15 * time.Second}, inventory: setting("BS_SEED_INVENTORY", "http://localhost:8101"), demand: setting("BS_SEED_DEMAND", "http://localhost:8102"), planning: setting("BS_SEED_PLANNING", "http://localhost:8103"), matching: setting("BS_SEED_MATCHING", "http://localhost:8104"), keycloak: setting("BS_SEED_KEYCLOAK", "http://localhost:8180"), aist: setting("BS_SEED_AIST", "http://localhost:8201")}
	ref, err := refdata.Load()
	if err != nil {
		return err
	}
	analyst, err := s.login("analyst1")
	if err != nil {
		return err
	}
	operator, err := s.login("operator-c001")
	if err != nil {
		return err
	}
	physician, err := s.login("physician1")
	if err != nil {
		return err
	}
	it, err := s.login("it1")
	if err != nil {
		return err
	}
	service, err := s.token(url.Values{"grant_type": {"client_credentials"}, "client_id": {"services"}, "client_secret": {setting("BS_SEED_SERVICE_SECRET", "services-demo-secret")}})
	if err != nil {
		return err
	}
	now := (clock.System{}).Now().UTC()
	forecastStart := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	year := forecastStart.Year()
	if err := s.json(http.MethodPost, s.aist+"/dev/seed", "", map[string]any{"seed": 42, "units": 10000}, nil); err != nil {
		return fmt.Errorf("запас АИСТ-М: %w", err)
	}
	if err := s.json(http.MethodPost, s.inventory+"/dev/seed-norms", it, map[string]any{"consumption_year": year - 1}, nil); err != nil {
		return fmt.Errorf("демо-нормативы: %w", err)
	}
	if err := s.waitSync(operator); err != nil {
		return fmt.Errorf("синхронизация АИСТ-М: %w", err)
	}
	csv := testdata.CSVConfig{Seed: 20261020, Year: year}
	profile, err := s.file(analyst, "DISTRICT_PROFILE", testdata.DistrictProfileCSV(ref, csv))
	if err != nil {
		return err
	}
	_, err = s.file(analyst, "CONSUMPTION_HISTORY", testdata.ConsumptionHistoryCSV(ref, csv))
	if err != nil {
		return err
	}
	sharesFile, err := s.file(analyst, "CLINIC_SHARE", testdata.ClinicShareCSV(ref, csv))
	if err != nil {
		return err
	}
	estimate, err := s.estimate(analyst, profile, sharesFile, year)
	if err != nil {
		return err
	}
	demo := true
	forecast := int02.EmergencyForecast{ScenarioVersion: "demo-seed-" + forecastStart.Format("200601"), ScenarioKind: "ROAD_ACCIDENT", ScenarioName: "Демонстрационный прогноз", Demo: &demo, Territory: int02.Territory{Name: "Демо-территория"}, PeriodStart: forecastStart, PeriodEnd: forecastStart.Add(24 * time.Hour), Lines: []int02.ForecastLine{{ComponentKind: "RBC", BloodGroup: "O+", VolumeML: 900, ClinicCode: ref.Clinics[1].ClinicCode}}}
	var receipt struct {
		ID uuid.UUID `json:"emergency_forecast_id"`
	}
	if err := s.json(http.MethodPost, s.planning+"/int/forecast", service, forecast, &receipt); err != nil {
		return fmt.Errorf("прогноз ЧС: %w", err)
	}
	planKey := uuid.NewSHA1(estimate, []byte(receipt.ID.String()))
	planKey, err = s.waitPlan(analyst, planKey, receipt.ID, estimate)
	if err != nil {
		return err
	}
	requestKey := uuid.NewSHA1(uuid.NameSpaceURL, []byte("demo-request-"+forecastStart.Format("200601")))
	var clinical struct {
		ID uuid.UUID `json:"clinical_request_id"`
	}
	if err := s.json(http.MethodPost, s.matching+"/requests", physician, map[string]any{"idempotency_key": requestKey, "patient_case_no": "DEMO-SEED-" + forecastStart.Format("200601"), "component_kind": "RBC", "requested_units": 1, "urgency": "URGENT", "blood_group": "O+", "phenotype": "", "constraints": []any{}}, &clinical); err != nil {
		return fmt.Errorf("демо-заявка: %w", err)
	}
	slog.Info("Демо подготовлено: выбор варианта и подтверждение выполняются пользователем", "plan_id", planKey, "request_id", clinical.ID)
	return nil
}
