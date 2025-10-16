package bitmex

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Упрощённый WS-каркас без внешних зависимостей.
// Соединение и чтение/запись должны быть внедрены позднее.
type WSClient struct {
	url       string
	apiKey    string
	apiSecret string

	connMu sync.RWMutex
	// conn абстрактен; позже будет реальный websocket.Conn
}

type WSConfig struct {
	URL       string // wss://www.bitmex.com/realtime
	APIKey    string
	APISecret string
}

func NewWSClient(cfg WSConfig) *WSClient {
	return &WSClient{
		url:       cfg.URL,
		apiKey:    cfg.APIKey,
		apiSecret: cfg.APISecret,
	}
}

func (w *WSClient) Connect(ctx context.Context) error {
	// Заглушка: подключение будет реализовано позже
	return nil
}

func (w *WSClient) Close() error {
	// Заглушка закрытия
	return nil
}

func (w *WSClient) Auth(ctx context.Context) error {
	if w.apiKey == "" || w.apiSecret == "" {
		return nil
	}
	expires := time.Now().Add(5 * time.Second).Unix()
	_ = signWS(w.apiSecret, "GET", "/realtime", expires)
	// Обычно здесь отправляется сообщение authKeyExpires через WS
	return nil
}

func signWS(secret, verb, path string, expires int64) string {
	payload := fmt.Sprintf("%s%s%d", verb, path, expires)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return encodingToHex(mac.Sum(nil))
}

func encodingToHex(b []byte) string { return hex.EncodeToString(b) }

func (w *WSClient) WriteJSON(v any) error {
	// Заглушка записи
	_ = v
	return nil
}

func (w *WSClient) Subscribe(channel string, symbol string) error {
	req := map[string]any{
		"op":   "subscribe",
		"args": []string{fmt.Sprintf("%s:%s", channel, symbol)},
	}
	return w.WriteJSON(req)
}

func (w *WSClient) ReadLoop(ctx context.Context, onMessage func(raw json.RawMessage), onError func(error)) {
	// Заглушка чтения
	_ = ctx
	_ = onMessage
	_ = onError
}
