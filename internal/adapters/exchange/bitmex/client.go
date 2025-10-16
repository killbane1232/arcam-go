package bitmex

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"arcam-go/internal/domain/entities"
	"arcam-go/internal/domain/value_objects"
	port "arcam-go/internal/ports/exchange"

	"github.com/shopspring/decimal"
)

// Client реализует port.ExchangeClient для BitMEX
type Client struct {
	httpClient *http.Client
	apiKey     string
	apiSecret  string
	baseURL    string
	limiter    *TokenBucket
}

var _ port.ExchangeClient = (*Client)(nil)

type Config struct {
	APIKey    string
	APISecret string
	BaseURL   string // e.g. https://www.bitmex.com
	Timeout   time.Duration
}

func NewClient(cfg Config) *Client {
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://www.bitmex.com"
	}
	return &Client{
		httpClient: &http.Client{Timeout: cfg.Timeout},
		apiKey:     cfg.APIKey,
		apiSecret:  cfg.APISecret,
		baseURL:    cfg.BaseURL,
		limiter:    NewTokenBucket(60, 1, time.Second),
	}
}

func (c *Client) Connect(ctx context.Context) error {
	// Для REST это no-op; для WS будет установлено отдельным компонентом
	return nil
}

func (c *Client) Close(ctx context.Context) error { return nil }

func (c *Client) PlaceLimitOrder(ctx context.Context, symbol string, side entities.OrderSide, price value_objects.Price, qty value_objects.Quantity) (*entities.Order, error) {
	body := map[string]any{
		"symbol":   symbol,
		"side":     mapSide(side),
		"ordType":  "Limit",
		"price":    toFloat(price.Value().String()),
		"orderQty": toFloat(qty.Value().String()),
	}
	resp, err := c.doRequest(ctx, http.MethodPost, "/api/v1/order", nil, body)
	if err != nil {
		return nil, err
	}
	return ParseOrder(resp)
}

func (c *Client) PlaceMarketOrder(ctx context.Context, symbol string, side entities.OrderSide, qty value_objects.Quantity) (*entities.Order, error) {
	body := map[string]any{
		"symbol":   symbol,
		"side":     mapSide(side),
		"ordType":  "Market",
		"orderQty": toFloat(qty.Value().String()),
	}
	resp, err := c.doRequest(ctx, http.MethodPost, "/api/v1/order", nil, body)
	if err != nil {
		return nil, err
	}
	return ParseOrder(resp)
}

func (c *Client) CancelOrder(ctx context.Context, symbol string, clientOrderID string) error {
	// BitMEX позволяет отмену по clOrdID через тело запроса
	body := map[string]any{
		"clOrdID": clientOrderID,
		"symbol":  symbol,
	}
	_, err := c.doRequest(ctx, http.MethodDelete, "/api/v1/order", nil, body)
	return err
}

func (c *Client) GetOrder(ctx context.Context, symbol string, clientOrderID string) (*entities.Order, error) {
	q := url.Values{}
	q.Set("symbol", symbol)
	q.Set("clOrdID", clientOrderID)
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/v1/order", q, nil)
	if err != nil {
		return nil, err
	}
	// BitMEX возвращает массив ордеров
	var arr []json.RawMessage
	if err := json.Unmarshal(resp, &arr); err == nil && len(arr) > 0 {
		return ParseOrder(arr[0])
	}
	// Попробуем как одиночный объект
	return ParseOrder(resp)
}

func (c *Client) GetPosition(ctx context.Context, symbol string) (*entities.Position, error) {
	// /api/v1/position?filter={"symbol":"XBTUSD"}
	filter := fmt.Sprintf("{\"symbol\":\"%s\"}", symbol)
	q := url.Values{}
	q.Set("filter", filter)
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/v1/position", q, nil)
	if err != nil {
		return nil, err
	}
	var positions []map[string]any
	if err := json.Unmarshal(resp, &positions); err != nil || len(positions) == 0 {
		return &entities.Position{Symbol: symbol}, nil
	}
	raw := positions[0]
	// поля: currentQty, avgEntryPrice, leverage
	size := toDecimal(raw["currentQty"]) // может быть отрицательным
	qty, _ := value_objects.NewQuantity(size)
	avg := toDecimal(raw["avgEntryPrice"])
	avgPrice, _ := value_objects.NewPrice(avg, "USD")
	lev := int(toFloat(fmt.Sprint(raw["leverage"])))
	pos, _ := entities.NewPosition(symbol, qty, avgPrice, lev)
	return pos, nil
}

func (c *Client) GetLastPrice(ctx context.Context, symbol string) (value_objects.Price, error) {
	// /api/v1/instrument?symbol=XBTUSD
	q := url.Values{}
	q.Set("symbol", symbol)
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/v1/instrument", q, nil)
	if err != nil {
		return value_objects.NewPriceFromString("0", "USD")
	}
	var arr []map[string]any
	if err := json.Unmarshal(resp, &arr); err != nil || len(arr) == 0 {
		return value_objects.NewPriceFromString("0", "USD")
	}
	last := toDecimal(arr[0]["lastPrice"])
	return value_objects.NewPrice(last, "USD")
}

// doRequest выполняет подписанный запрос к BitMEX REST API
func (c *Client) doRequest(ctx context.Context, method, path string, query url.Values, body any) ([]byte, error) {
	// Собираем URL
	fullURL := c.baseURL + path
	if query != nil && len(query) > 0 {
		fullURL += "?" + query.Encode()
	}
	var bodyReader io.Reader
	var bodyBytes []byte
	var err error
	if body != nil {
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}
	var respBytes []byte
	err = Retry(ctx, 3, 200*time.Millisecond, func() error {
		if c.limiter != nil {
			c.limiter.Take()
		}
		req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
		if err != nil {
			return err
		}
		// Подпись BitMEX: sign = HMAC_SHA256(secret, verb + path + query + expires + body)
		expires := time.Now().Add(5 * time.Second).Unix()
		pathWithQuery := path
		if query != nil && len(query) > 0 {
			pathWithQuery += "?" + query.Encode()
		}
		signature := signRequest(c.apiSecret, method, pathWithQuery, expires, string(bodyBytes))
		req.Header.Set("api-key", c.apiKey)
		req.Header.Set("api-expires", strconv.FormatInt(expires, 10))
		req.Header.Set("api-signature", signature)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		respBytes, err = io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return fmt.Errorf("retryable status %d: %s", resp.StatusCode, string(respBytes))
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return StopRetry(fmt.Errorf("bitmex http %d: %s", resp.StatusCode, string(respBytes)))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return respBytes, nil
}

func signRequest(secret, verb, pathWithQuery string, expires int64, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(verb))
	mac.Write([]byte(pathWithQuery))
	mac.Write([]byte(strconv.FormatInt(expires, 10)))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

// Вспомогательные функции маппинга типов и безопасных конверсий
func mapSide(side entities.OrderSide) string {
	if side == entities.OrderSideBuy {
		return "Buy"
	}
	return "Sell"
}

// toFloat парсит строковое представление decimal в float64 для тела запроса
func toFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// toDecimal извлекает shopspring/decimal.Decimal из произвольного JSON-значения
func toDecimal(v any) decimal.Decimal {
	switch t := v.(type) {
	case float64:
		return decimal.NewFromFloat(t)
	case float32:
		return decimal.NewFromFloat(float64(t))
	case int:
		return decimal.NewFromInt(int64(t))
	case int64:
		return decimal.NewFromInt(t)
	case json.Number:
		f, _ := t.Float64()
		return decimal.NewFromFloat(f)
	case string:
		d, err := decimal.NewFromString(t)
		if err != nil {
			return decimal.Zero
		}
		return d
	default:
		return decimal.Zero
	}
}
