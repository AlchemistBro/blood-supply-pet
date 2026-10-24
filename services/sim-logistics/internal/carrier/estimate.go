package carrier

import (
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/clock"
)

// Clinic — клиника для расчёта оценок: важен только район.
type Clinic struct {
	ClinicID   int64
	DistrictID int64
}

// Estimate — оценка времени доставки до одной клиники-получателя.
type Estimate struct {
	TargetClinicID int64
	Minutes        int64
}

// Estimates — оценки от одной клиники-источника.
type Estimates struct {
	SourceClinicID int64
	Estimates      []Estimate
	EstimatedAt    time.Time
}

// Estimator считает синтетические оценки времени доставки (демо, ADR-0010). Расчёт детерминирован и
// симметричен: одна и та же пара клиник всегда даёт одно и то же число минут в обе стороны,
// поэтому тесты и демо воспроизводимы.
type Estimator struct {
	clock   clock.Clock
	byID    map[int64]Clinic
	ordered []int64
}

// Параметры синтетической модели (демо): база внутри района, база между районами, шаг за район, разброс пары.
const (
	sameDistrictBaseMinutes  = 15
	crossDistrictBaseMinutes = 30
	perDistrictStepMinutes   = 10
	pairJitterMinutes        = 10
)

// NewEstimator создаёт расчёт по списку клиник.
func NewEstimator(clinics []Clinic, c clock.Clock) *Estimator {
	e := &Estimator{clock: c, byID: make(map[int64]Clinic, len(clinics))}
	for _, cl := range clinics {
		if _, dup := e.byID[cl.ClinicID]; dup {
			continue
		}
		e.byID[cl.ClinicID] = cl
		e.ordered = append(e.ordered, cl.ClinicID)
	}
	slices.Sort(e.ordered)
	return e
}

// Estimate возвращает оценку от source до target; target = 0 — до всех остальных клиник.
func (e *Estimator) Estimate(source, target int64) (Estimates, error) {
	src, ok := e.byID[source]
	if !ok {
		return Estimates{}, fmt.Errorf("клиника-источник %d: %w", source, ErrNotFound)
	}
	out := Estimates{SourceClinicID: source, EstimatedAt: e.clock.Now(), Estimates: []Estimate{}}
	if target != 0 {
		dst, ok := e.byID[target]
		if !ok {
			return Estimates{}, fmt.Errorf("клиника-получатель %d: %w", target, ErrNotFound)
		}
		if target == source {
			return Estimates{}, fmt.Errorf("%w: источник и получатель совпадают", ErrInvalid)
		}
		out.Estimates = append(out.Estimates, Estimate{TargetClinicID: target, Minutes: minutes(src, dst)})
		return out, nil
	}
	for _, id := range e.ordered {
		if id == source {
			continue
		}
		out.Estimates = append(out.Estimates, Estimate{TargetClinicID: id, Minutes: minutes(src, e.byID[id])})
	}
	return out, nil
}

func minutes(a, b Clinic) int64 {
	lo, hi := min(a.ClinicID, b.ClinicID), max(a.ClinicID, b.ClinicID)
	h := fnv.New32a()
	_, _ = h.Write([]byte(strconv.FormatInt(lo, 10) + "-" + strconv.FormatInt(hi, 10)))
	jitter := int64(h.Sum32() % pairJitterMinutes)
	if a.DistrictID == b.DistrictID {
		return sameDistrictBaseMinutes + jitter
	}
	dist := a.DistrictID - b.DistrictID
	if dist < 0 {
		dist = -dist
	}
	return crossDistrictBaseMinutes + perDistrictStepMinutes*dist + jitter
}
