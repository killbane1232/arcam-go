package entities

import (
	"errors"
	"math/rand"
	"time"

	"arcam-go/internal/domain/value_objects"
)

type Trade struct {
	ID        string
	OrderID   string
	Symbol    string
	Side      OrderSide
	Price     value_objects.Price
	Quantity  value_objects.Quantity
	Fee       value_objects.Money
	Timestamp time.Time
}

func NewTrade(orderID string, symbol string, side OrderSide,
	price value_objects.Price, quantity value_objects.Quantity,
	fee value_objects.Money) (*Trade, error) {

	if orderID == "" {
		return nil, errors.New("orderID cannot be empty")
	}
	if symbol == "" {
		return nil, errors.New("symbol cannot be empty")
	}

	now := time.Now().UTC()
	return &Trade{
		ID:        generateTradeID(),
		OrderID:   orderID,
		Symbol:    symbol,
		Side:      side,
		Price:     price,
		Quantity:  quantity,
		Fee:       fee,
		Timestamp: now,
	}, nil
}

func (t *Trade) TotalCost() (value_objects.Money, error) {
	return t.Price.Multiply(t.Quantity)
}

func generateTradeID() string {
	return "trade_" + time.Now().Format("20060102150405") + "_" + string(rune(rand.Intn(1000)))
}
