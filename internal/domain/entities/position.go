package entities

import (
	"errors"
	"time"

	"arcam-go/internal/domain/value_objects"

	"github.com/shopspring/decimal"
)

type Position struct {
	Symbol        string
	Size          value_objects.Quantity
	AvgEntryPrice value_objects.Price
	Leverage      int
	CreatedAt     time.Time
	UpdatedAt     time.Time
	CurrentPrice  value_objects.Price
}

func NewPosition(symbol string, size value_objects.Quantity,
	entryPrice value_objects.Price, leverage int) (*Position, error) {

	if symbol == "" {
		return nil, errors.New("symbol cannot be empty")
	}
	if leverage <= 0 {
		return nil, errors.New("leverage must be positive")
	}

	now := time.Now().UTC()
	return &Position{
		Symbol:        symbol,
		Size:          size,
		AvgEntryPrice: entryPrice,
		Leverage:      leverage,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func (p *Position) IsLong() bool {
	return p.Size.Value().Sign() > 0
}

func (p *Position) IsShort() bool {
	return p.Size.Value().Sign() < 0
}

func (p *Position) IsFlat() bool {
	return p.Size.Value().IsZero()
}

func (p *Position) UpdatePrice(currentPrice value_objects.Price) {
	p.CurrentPrice = currentPrice
	p.UpdatedAt = time.Now().UTC()
}

func (p *Position) CalculateUnrealizedPnL() (value_objects.Money, error) {
	if p.IsFlat() || p.CurrentPrice.Value().IsZero() {
		return value_objects.ZeroMoney("USD")
	}

	size := p.Size.Value()
	entry := p.AvgEntryPrice.Value()
	current := p.CurrentPrice.Value()

	var pnl decimal.Decimal
	if p.IsLong() {
		pnl = size.Mul(current.Sub(entry))
	} else {
		pnl = size.Mul(entry.Sub(current))
	}

	return value_objects.NewMoney(pnl, "USD")
}

func (p *Position) CalculateLiquidationPrice() (value_objects.Price, error) {
	if p.IsFlat() {
		return value_objects.NewPrice(decimal.Zero, "USD")
	}

	entry := p.AvgEntryPrice.Value()
	leverage := decimal.NewFromInt(int64(p.Leverage))

	var liqPrice decimal.Decimal
	if p.IsLong() {
		liqPrice = entry.Mul(decimal.NewFromInt(1).Sub(decimal.NewFromInt(1).Div(leverage)))
	} else {
		liqPrice = entry.Mul(decimal.NewFromInt(1).Add(decimal.NewFromInt(1).Div(leverage)))
	}

	return value_objects.NewPrice(liqPrice, p.AvgEntryPrice.Currency())
}
