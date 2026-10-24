package c03

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/AlchemistBro/blood-supply-pet/libs/contracts/logistics"
)

func (c *Client) Estimates(ctx context.Context, source int64) (logistics.DeliveryEstimatesPublished, error) {
	var response struct {
		Source    int64     `json:"sourceClinicId"`
		At        time.Time `json:"estimatedAt"`
		Estimates []struct {
			Target  int64 `json:"targetClinicId"`
			Minutes int   `json:"minutes"`
		} `json:"estimates"`
	}
	status, err := c.call(ctx, http.MethodGet, "/estimates?sourceClinicId="+strconv.FormatInt(source, 10), nil, &response)
	if err != nil {
		return logistics.DeliveryEstimatesPublished{}, err
	}
	if status != http.StatusOK || response.Source != source || response.At.IsZero() {
		return logistics.DeliveryEstimatesPublished{}, unavailable("неверная оценка доставки C03")
	}
	out := logistics.DeliveryEstimatesPublished{SourceClinicID: source, EstimatedAt: response.At, Estimates: []logistics.DeliveryEstimate{}}
	seen := map[int64]bool{}
	for _, line := range response.Estimates {
		if line.Target <= 0 || line.Target == source || line.Minutes <= 0 || line.Minutes > 525600 || seen[line.Target] {
			return logistics.DeliveryEstimatesPublished{}, unavailable("некорректный маршрут C03")
		}
		seen[line.Target] = true
		out.Estimates = append(out.Estimates, logistics.DeliveryEstimate{TargetClinicID: line.Target, Minutes: line.Minutes})
	}
	return out, nil
}
