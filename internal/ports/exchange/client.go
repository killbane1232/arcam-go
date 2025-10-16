package exchange

import (
	"context"

	"arcam-go/internal/domain/entities"
	"arcam-go/internal/domain/value_objects"
)

// ExchangeClient описывает минимальный набор операций биржевого клиента,
// необходимых торговому движку и прикладным слоям.
type ExchangeClient interface {
	// Connectivity
	Connect(ctx context.Context) error
	Close(ctx context.Context) error

	// Orders
	PlaceLimitOrder(ctx context.Context, symbol string, side entities.OrderSide, price value_objects.Price, qty value_objects.Quantity) (*entities.Order, error)
	PlaceMarketOrder(ctx context.Context, symbol string, side entities.OrderSide, qty value_objects.Quantity) (*entities.Order, error)
	CancelOrder(ctx context.Context, symbol string, clientOrderID string) error
	GetOrder(ctx context.Context, symbol string, clientOrderID string) (*entities.Order, error)

	// Positions
	GetPosition(ctx context.Context, symbol string) (*entities.Position, error)

	// Market Data (on-demand)
	GetLastPrice(ctx context.Context, symbol string) (value_objects.Price, error)
}

