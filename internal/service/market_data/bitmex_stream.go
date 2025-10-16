package market_data

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// BitmexStream - каркас реализации Stream для BitMEX WS
type BitmexStream struct {
	events chan StreamEvent
	errs   chan error

	wsURL   string
	binSize string

	mu     sync.RWMutex
	conn   *websocket.Conn
	dialer *websocket.Dialer

	subscribeSymbol string
}

func NewBitmexStream(wsURL string, binSize string) *BitmexStream {
	if wsURL == "" {
		wsURL = "wss://www.bitmex.com/realtime"
	}
	if binSize == "" {
		binSize = "1m"
	}
	return &BitmexStream{
		events:  make(chan StreamEvent, 256),
		errs:    make(chan error, 16),
		wsURL:   wsURL,
		binSize: binSize,
		dialer: &websocket.Dialer{
			HandshakeTimeout:  15 * time.Second,
			EnableCompression: false,
			ReadBufferSize:    1024,
			WriteBufferSize:   1024,
		},
	}
}

func (b *BitmexStream) Connect(ctx context.Context) error {
	// использовать системный прокси
	b.dialer.Proxy = http.ProxyFromEnvironment

	go b.manageConnection(ctx)
	return nil
}

func (b *BitmexStream) Close(ctx context.Context) error {
	b.mu.Lock()
	if b.conn != nil {
		_ = b.conn.Close()
		b.conn = nil
	}
	b.mu.Unlock()
	close(b.events)
	close(b.errs)
	return nil
}

func (b *BitmexStream) SubscribeTrades(ctx context.Context, symbol string) error {
	// Используем как подписку на свечи (tradeBin)
	b.mu.Lock()
	b.subscribeSymbol = symbol
	conn := b.conn
	bin := b.binSize
	b.mu.Unlock()
	if conn != nil {
		return b.subscribeCandles(symbol, bin)
	}
	return nil
}

func (b *BitmexStream) SubscribeOrderBookL2(ctx context.Context, symbol string) error {
	// Делегирование в WS адаптер
	return nil
}

func (b *BitmexStream) Events() <-chan StreamEvent {
	return b.events
}

func (b *BitmexStream) Errors() <-chan error {
	return b.errs
}

func (b *BitmexStream) subscribeCandles(symbol, binSize string) error {
	log.Printf("bitmex ws subscribe: tradeBin%s:%s", binSize, symbol)
	b.mu.RLock()
	conn := b.conn
	b.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("ws not connected")
	}
	topic := fmt.Sprintf("tradeBin%s:%s", binSize, symbol)
	sub := map[string]any{
		"op":   "subscribe",
		"args": []string{topic},
	}
	return conn.WriteJSON(sub)
}

func (b *BitmexStream) readLoop(ctx context.Context) {
	type wsCandle struct {
		Timestamp time.Time `json:"timestamp"`
		Open      float64   `json:"open"`
		High      float64   `json:"high"`
		Low       float64   `json:"low"`
		Close     float64   `json:"close"`
		Volume    float64   `json:"volume"`
	}
	type wsResp struct {
		Table  string     `json:"table"`
		Action string     `json:"action"`
		Data   []wsCandle `json:"data"`
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		b.mu.RLock()
		conn := b.conn
		sym := b.subscribeSymbol
		b.mu.RUnlock()
		if conn == nil {
			return
		}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			select {
			case b.errs <- err:
			default:
			}
			log.Printf("bitmex ws read error: %v", err)
			return
		}
		var resp wsResp
		if err := json.Unmarshal(msg, &resp); err != nil {
			// не структурное сообщение (auth/пинги и т.п.)
			continue
		}
		if resp.Table == "" || len(resp.Data) == 0 {
			continue
		}
		if resp.Action == "partial" || resp.Action == "insert" {
			log.Printf("bitmex ws recv %s: %d candles", resp.Action, len(resp.Data))
			for _, c := range resp.Data {
				evCandle := Candle{
					Timestamp: c.Timestamp.Unix(),
					Open:      c.Open,
					High:      c.High,
					Low:       c.Low,
					Close:     c.Close,
					Volume:    c.Volume,
				}
				log.Printf("bitmex ws candle %s %s O=%.2f H=%.2f L=%.2f C=%.2f V=%.2f",
					sym, c.Timestamp.UTC().Format(time.RFC3339), c.Open, c.High, c.Low, c.Close, c.Volume)
				raw, _ := json.Marshal(evCandle)
				b.events <- StreamEvent{Channel: "candle", Symbol: sym, Raw: raw}
			}
		}
	}
}

func (b *BitmexStream) manageConnection(ctx context.Context) {
	backoff := 5 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		log.Printf("bitmex ws connecting to %s", b.wsURL)
		conn, _, err := b.dialer.DialContext(ctx, b.wsURL, nil)
		if err != nil {
			// сообщаем и ждем перед повтором
			select {
			case b.errs <- err:
			default:
			}
			log.Printf("bitmex ws dial error: %v; retry in %s", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			// экспоненциальный бэкоф до 1 мин
			if backoff < time.Minute {
				backoff *= 2
			}
			continue
		}
		backoff = 5 * time.Second

		b.mu.Lock()
		b.conn = conn
		b.mu.Unlock()
		log.Printf("bitmex ws connected")

		// keepalive ping/pong
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		})
		pingTicker := time.NewTicker(5 * time.Second)
		done := make(chan struct{})
		go func() {
			defer pingTicker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-done:
					return
				case <-pingTicker.C:
					_ = conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(3*time.Second))
				}
			}
		}()

		// подписка
		b.mu.RLock()
		sym := b.subscribeSymbol
		bin := b.binSize
		b.mu.RUnlock()
		if sym != "" {
			if err := b.subscribeCandles(sym, bin); err != nil {
				select {
				case b.errs <- err:
				default:
				}
				log.Printf("bitmex ws subscribe error: %v", err)
			}
		}

		// цикл чтения до ошибки
		b.readLoop(ctx)
		close(done)

		// закрыть и перейти к ретраю
		b.mu.Lock()
		if b.conn != nil {
			_ = b.conn.Close()
			b.conn = nil
		}
		b.mu.Unlock()
		log.Printf("bitmex ws disconnected; will retry")
	}
}
