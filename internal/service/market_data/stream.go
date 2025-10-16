package market_data

import "context"

// StreamEvent тип события рыночного потока
type StreamEvent struct {
	Channel string
	Symbol  string
	// Raw может содержать произвольные данные канала (например, orderBookL2, trade)
	Raw any
}

// Stream описывает унифицированный интерфейс подписки на маркет-данные
type Stream interface {
	Connect(ctx context.Context) error
	Close(ctx context.Context) error

	SubscribeTrades(ctx context.Context, symbol string) error
	SubscribeOrderBookL2(ctx context.Context, symbol string) error

	// Events возвращает канал событий. Канал закрывается при Close().
	Events() <-chan StreamEvent
	// Errors возвращает канал ошибок WS уровня.
	Errors() <-chan error
}

// Candle представляет унифицированную свечу для стрима
type Candle struct {
	Timestamp int64   `json:"timestamp"`
	Open      float64 `json:"open"`
	High      float64 `json:"high"`
	Low       float64 `json:"low"`
	Close     float64 `json:"close"`
	Volume    float64 `json:"volume"`
}
