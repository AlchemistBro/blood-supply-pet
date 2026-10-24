package c03

import (
	"context"
	"net/http"
	"net/url"

	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/app"
	"github.com/AlchemistBro/blood-supply-pet/services/logistics-gateway/internal/domain"
)

func (c *Client) Progress(ctx context.Context, row app.ProgressOrder) (app.ShipmentProgress, error) {
	var list struct {
		Shipments []struct {
			ShipmentRef string               `json:"shipmentRef"`
			Cancelled   bool                 `json:"cancelled"`
			Progress    app.ShipmentProgress `json:"progress"`
		} `json:"shipments"`
	}
	status, err := c.call(ctx, http.MethodGet, "/shipments?clientRef="+url.QueryEscape(domain.ClientRef(row.ID)), nil, &list)
	if err != nil {
		return app.ShipmentProgress{}, err
	}
	if status != http.StatusOK {
		return app.ShipmentProgress{}, unavailable("факты перевозки: C03 ответила %d", status)
	}
	for _, shipment := range list.Shipments {
		if shipment.ShipmentRef == row.ShipmentRef && !shipment.Cancelled {
			return shipment.Progress, nil
		}
	}
	return app.ShipmentProgress{}, nil
}
