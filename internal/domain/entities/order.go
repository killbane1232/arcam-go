package entities

import (
	"errors"
	"math/rand"
	"time"

	"arcam-go/internal/domain/value_objects"

	"github.com/shopspring/decimal"
)

type OrderType string

const (
	OrderTypeMarket OrderType = "market"
	OrderTypeLimit  OrderType = "limit"
)

type OrderSide string

const (
	OrderSideBuy  OrderSide = "buy"
	OrderSideSell OrderSide = "sell"
)

type OrderStatus string

const (
	OrderStatusNew             OrderStatus = "new"
	OrderStatusPartiallyFilled OrderStatus = "partially_filled"
	OrderStatusFilled          OrderStatus = "filled"
	OrderStatusCancelled       OrderStatus = "cancelled"
	OrderStatusRejected        OrderStatus = "rejected"
)

type Order struct {
	ID              string
	Symbol          string
	Side            OrderSide
	Type            OrderType
	Price           value_objects.Price
	Quantity        value_objects.Quantity
	Status          OrderStatus
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ExchangeOrderID string
	FilledQuantity  value_objects.Quantity
}

func NewOrder(symbol string, side OrderSide, orderType OrderType,
	price value_objects.Price, quantity value_objects.Quantity) (*Order, error) {

	if symbol == "" {
		return nil, errors.New("symbol cannot be empty")
	}

	if orderType == OrderTypeMarket && !price.Value().IsZero() {
		return nil, errors.New("market orders cannot have price")
	}

	if orderType == OrderTypeLimit && price.Value().IsZero() {
		return nil, errors.New("limit orders must have price")
	}

	// Создаем нулевое количество для filled quantity
	zeroQty, err := value_objects.NewQuantity(decimal.Zero)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	return &Order{
		ID:             generateOrderID(),
		Symbol:         symbol,
		Side:           side,
		Type:           orderType,
		Price:          price,
		Quantity:       quantity,
		Status:         OrderStatusNew,
		CreatedAt:      now,
		UpdatedAt:      now,
		FilledQuantity: zeroQty,
	}, nil
}

func (o *Order) IsActive() bool {
	return o.Status == OrderStatusNew || o.Status == OrderStatusPartiallyFilled
}

func (o *Order) Fill(quantity value_objects.Quantity) error {
	if quantity.GreaterThan(o.Quantity) {
		return errors.New("fill quantity exceeds order quantity")
	}

	o.FilledQuantity = quantity
	if o.FilledQuantity.Equal(o.Quantity) {
		o.Status = OrderStatusFilled
	} else {
		o.Status = OrderStatusPartiallyFilled
	}
	o.UpdatedAt = time.Now().UTC()
	return nil
}

func (o *Order) Cancel() error {
	if !o.IsActive() {
		return errors.New("cannot cancel non-active order")
	}
	o.Status = OrderStatusCancelled
	o.UpdatedAt = time.Now().UTC()
	return nil
}

func (o *Order) RemainingQuantity() value_objects.Quantity {
	remaining := o.Quantity.Value().Sub(o.FilledQuantity.Value())
	newQty, err := value_objects.NewQuantity(remaining)
	if err != nil {
		// В случае ошибки возвращаем нулевое количество
		zero, _ := value_objects.NewQuantity(decimal.Zero)
		return zero
	}
	return newQty
}

func generateOrderID() string {
	return "order_" + time.Now().Format("20060102150405") + "_" + string(rune(rand.Intn(1000)))
}
