package broker

import "context"

type Broker interface {
	Name() string
}

type KalshiOrder struct {
	Ticker        string
	Side          string
	Action        string
	Count         int
	YesPriceFloat float64
	ClientOrderID string
}

type KalshiOrderResponse struct {
	OrderID string
	Status  string
}

type KalshiOrderPlacer interface {
	PlaceOrder(ctx context.Context, order KalshiOrder) (KalshiOrderResponse, error)
}
