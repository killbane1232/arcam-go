package bitmex

import (
	"encoding/json"

	"arcam-go/internal/domain/entities"
	"arcam-go/internal/domain/value_objects"

	"github.com/shopspring/decimal"
)

// ParseOrder минимальный парсер ответа BitMEX ордера в доменную сущность
// Ожидаемый формат на основе BitMEX REST/WS полей: orderID, symbol, side, ordType, price, orderQty, cumQty, ordStatus
func ParseOrder(data []byte) (*entities.Order, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	symbol, _ := raw["symbol"].(string)
	sideStr, _ := raw["side"].(string)
	ordTypeStr, _ := raw["ordType"].(string)
	ordStatusStr, _ := raw["ordStatus"].(string)

	var side entities.OrderSide
	switch sideStr {
	case "Buy":
		side = entities.OrderSideBuy
	case "Sell":
		side = entities.OrderSideSell
	}

	var ordType entities.OrderType
	switch ordTypeStr {
	case "Limit":
		ordType = entities.OrderTypeLimit
	case "Market":
		ordType = entities.OrderTypeMarket
	}

	priceDec := decimal.Zero
	if v, ok := raw["price"].(float64); ok {
		priceDec = decimal.NewFromFloat(v)
	}
	price, _ := value_objects.NewPrice(priceDec, "USD")

	qtyDec := decimal.Zero
	if v, ok := raw["orderQty"].(float64); ok {
		qtyDec = decimal.NewFromFloat(v)
	}
	qty, _ := value_objects.NewQuantity(qtyDec)

	cumDec := decimal.Zero
	if v, ok := raw["cumQty"].(float64); ok {
		cumDec = decimal.NewFromFloat(v)
	}
	filledQty, _ := value_objects.NewQuantity(cumDec)

	order := &entities.Order{
		Symbol:          symbol,
		Side:            side,
		Type:            ordType,
		Price:           price,
		Quantity:        qty,
		ExchangeOrderID: stringFromAny(raw["orderID"]),
		Status:          mapStatus(ordStatusStr),
		FilledQuantity:  filledQty,
	}
	return order, nil
}

func stringFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func mapStatus(s string) entities.OrderStatus {
	switch s {
	case "New":
		return entities.OrderStatusNew
	case "PartiallyFilled", "PartiallyFilledHidden":
		return entities.OrderStatusPartiallyFilled
	case "Filled":
		return entities.OrderStatusFilled
	case "Canceled", "Cancelled":
		return entities.OrderStatusCancelled
	case "Rejected":
		return entities.OrderStatusRejected
	default:
		return entities.OrderStatusNew
	}
}

