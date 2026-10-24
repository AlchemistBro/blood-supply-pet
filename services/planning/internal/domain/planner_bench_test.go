package domain_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
	"github.com/AlchemistBro/blood-supply-pet/libs/refdata"
	"github.com/AlchemistBro/blood-supply-pet/libs/testdata"
	"github.com/AlchemistBro/blood-supply-pet/services/planning/internal/domain"
)

// nfr002Limit — NFR002: расчёт для 100 клиник и 100 000 единиц не дольше 30 с. Здесь меряется только
// доменный расчёт в памяти; полный бюджет плана (проекции, запись, outbox) проверяет task bench:plan.
const nfr002Limit = 30 * time.Second

// benchInput собирает приёмочный набор NFR002 из генератора синтетических данных (ADR-0010):
// 100 клиник, 12 районов, 4 вида, 8 групп, 100 000 единиц; норматив из годового расхода (FR012),
// фон — недельная доля расхода, прогноз ЧС по трём районам и по пяти принимающим клиникам, полная
// матрица оценок доставки.
func benchInput(b *testing.B) domain.Input {
	b.Helper()
	ref := refdata.MustLoad()
	cfg := testdata.DefaultConfig()
	ds := testdata.Generate(ref, cfg)

	clinicID := map[string]int64{}
	district := map[int64]int64{}
	perDistrict := map[int64]int{}
	for _, c := range ref.Clinics {
		clinicID[c.ClinicCode] = c.ClinicID
		district[c.ClinicID] = c.DistrictID
		perDistrict[c.DistrictID]++
	}
	in := domain.Input{
		PlanID: uuid.NewSHA1(uuid.NameSpaceURL, []byte("bench-nfr002")),
		Versions: domain.InputVersions{
			DemandEstimateID: uuid.NewSHA1(uuid.NameSpaceURL, []byte("bench-demand")), DemandVersionNo: 1,
			NormVersion: 1, StockWatermark: 1, StockSyncedAt: cfg.Now, RefdataVersion: ref.Version,
		},
		Params: domain.DefaultParams(),
	}
	typical := map[string]int64{}
	for _, k := range ref.ComponentKinds {
		in.Kinds = append(in.Kinds, domain.Kind{Code: k.Code, TypicalVolumeML: k.TypicalVolumeML, GroupApplicable: k.IsGroupApplicable})
		typical[k.Code] = k.TypicalVolumeML
	}
	for _, g := range ref.BloodGroups {
		in.GroupShares = append(in.GroupShares, domain.GroupShare{Group: g.Code, Share: g.PopulationShare})
	}
	for _, c := range ref.Clinics {
		in.ClinicShares = append(in.ClinicShares, domain.ClinicShare{
			DistrictID: c.DistrictID, ClinicID: c.ClinicID, Share: 1 / float64(perDistrict[c.DistrictID]),
		})
	}
	for _, a := range ds.Consumption {
		id := clinicID[a.ClinicCode]
		norm := max(float64(a.VolumeML)/360, float64(typical[a.ComponentKind]))
		in.Norms = append(in.Norms, domain.Norm{ClinicID: id, Kind: a.ComponentKind, NormML: norm})
		in.Background = append(in.Background, domain.Background{ClinicID: id, Kind: a.ComponentKind, VolumeML: float64(a.VolumeML) / 52})
	}
	for _, u := range ds.Units {
		in.Units = append(in.Units, domain.Unit{
			UnitID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(u.AistUnitCode)), ClinicID: clinicID[u.ClinicCode],
			Kind: u.ComponentKind, Group: u.BloodGroup, VolumeML: u.VolumeML, ExpiresAt: u.ExpiresAt, Status: u.Status,
		})
	}
	forecast := &domain.Forecast{ForecastID: uuid.NewSHA1(uuid.NameSpaceURL, []byte("bench-forecast")), ScenarioVersion: "bench-1"}
	for _, d := range ref.Districts[:3] {
		forecast.Territory = append(forecast.Territory, domain.DistrictWeight{DistrictID: d.DistrictID, Weight: float64(d.Population)})
	}
	line := 0
	addLine := func(clinic int64, kind, group string, volume float64) {
		line++
		forecast.Lines = append(forecast.Lines, domain.ForecastLine{
			LineID: uuid.NewSHA1(uuid.NameSpaceURL, []byte{byte(line)}), ClinicID: clinic, Kind: kind, Group: group, VolumeML: volume,
		})
	}
	for _, l := range []struct {
		kind   string
		volume float64
	}{{"RBC", 300_000}, {"FFP", 120_000}, {"PLT", 40_000}, {"CRYO", 4_000}} {
		addLine(0, l.kind, "", l.volume)
	}
	for _, c := range ref.Clinics[:5] {
		addLine(c.ClinicID, "RBC", "O+", 6_000)
		addLine(c.ClinicID, "RBC", "O-", 1_500)
	}
	in.Forecast = forecast
	for _, s := range ref.Clinics {
		for _, t := range ref.Clinics {
			if s.ClinicID == t.ClinicID {
				continue
			}
			hops := district[s.ClinicID] - district[t.ClinicID]
			if hops < 0 {
				hops = -hops
			}
			in.Delivery = append(in.Delivery, domain.DeliveryEstimate{
				SourceClinicID: s.ClinicID, TargetClinicID: t.ClinicID,
				Minutes:     int(20 + 10*hops + (s.ClinicID+t.ClinicID)%7),
				EstimatedAt: cfg.Now.Add(-10 * time.Minute),
			})
		}
	}
	return in
}

// BenchmarkBuildPlanNFR002 — расчёт плана на приёмочном наборе NFR002 (task bench:plan).
func BenchmarkBuildPlanNFR002(b *testing.B) {
	in := benchInput(b)
	planner := domain.NewPlanner(clock.NewMutable(testdata.DefaultConfig().Now))
	b.ReportAllocs()
	var plan *domain.Plan
	for b.Loop() {
		p, err := planner.Build(in)
		if err != nil {
			b.Fatalf("расчёт плана: %v", err)
		}
		plan = p
	}
	if per := b.Elapsed() / time.Duration(b.N); per > nfr002Limit {
		b.Fatalf("NFR002: расчёт занял %s, предел %s", per, nfr002Limit)
	}
	b.ReportMetric(plan.CoverageRatio, "coverage")
	b.ReportMetric(float64(len(plan.Proposals)), "proposals")
	b.ReportMetric(float64(len(plan.Deficits)), "deficits")
}
