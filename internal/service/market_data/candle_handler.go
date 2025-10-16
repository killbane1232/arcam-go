package market_data

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/shopspring/decimal"

	ind "arcam-go/internal/service/indicator"
	tb "arcam-go/internal/service/telebot"
)

// CandleSignalHandler агрегирует свечи, считает индикаторы и отправляет результат в Telegram
type CandleSignalHandler struct {
	stream   Stream
	registry *ind.Registry
	symbol   string
	chatBot  *tb.TeleBot
	baseURL  string
	binSize  string

	// окно данных (FIFO) для индикаторов
	window []ind.OHLCV
	// максимальный размер окна = макс(period) зарегистрированных индикаторов + запас
	windowSize int
}

func NewCandleSignalHandler(stream Stream, registry *ind.Registry, symbol string, chatBot *tb.TeleBot, baseURL string, binSize string, windowSize int) *CandleSignalHandler {
	if windowSize <= 0 {
		windowSize = 200
	}
	return &CandleSignalHandler{
		stream:     stream,
		registry:   registry,
		symbol:     symbol,
		chatBot:    chatBot,
		baseURL:    baseURL,
		binSize:    binSize,
		window:     make([]ind.OHLCV, 0, windowSize),
		windowSize: windowSize,
	}
}

func (h *CandleSignalHandler) Start(ctx context.Context) error {
	// Начальная подкачка истории из REST
	if err := h.backfillRecent(ctx, h.windowSize); err != nil {
		log.Printf("rest backfill error: %v", err)
		if h.chatBot != nil {
			_ = h.chatBot.SendText(fmt.Sprintf("REST backfill error: %v", err))
		}
	}
	if err := h.stream.Connect(ctx); err != nil {
		log.Printf("stream connect error: %v", err)
		return err
	}
	if err := h.stream.SubscribeTrades(ctx, h.symbol); err != nil { // используем как подписку на свечи
		log.Printf("stream subscribe error: %v", err)
		return err
	}

	go h.loop(ctx)
	return nil
}

func (h *CandleSignalHandler) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-h.stream.Events():
			if !ok {
				return
			}
			if evt.Channel != "candle" {
				continue
			}
			var c Candle
			if raw, ok := evt.Raw.([]byte); ok {
				if err := json.Unmarshal(raw, &c); err != nil {
					continue
				}
			} else if rawJ, ok := evt.Raw.(json.RawMessage); ok {
				if err := json.Unmarshal(rawJ, &c); err != nil {
					continue
				}
			} else {
				// попытка привести напрямую
				if cast, ok := evt.Raw.(Candle); ok {
					c = cast
				} else {
					continue
				}
			}

			log.Printf("handler recv candle ts=%d o=%.2f h=%.2f l=%.2f c=%.2f v=%.2f", c.Timestamp, c.Open, c.High, c.Low, c.Close, c.Volume)
			h.backfillIfGap(ctx, c)
			h.pushCandle(c)
			h.calculateAndSend(ctx)
		case err := <-h.stream.Errors():
			log.Printf("stream error: %v", err)
		}
	}
}

func (h *CandleSignalHandler) pushCandle(c Candle) {
	log.Printf("push candle %s ts=%d", h.symbol, c.Timestamp)
	o := ind.OHLCV{
		Timestamp: time.Unix(c.Timestamp, 0).UTC(),
		Open:      decimal.NewFromFloat(c.Open),
		High:      decimal.NewFromFloat(c.High),
		Low:       decimal.NewFromFloat(c.Low),
		Close:     decimal.NewFromFloat(c.Close),
		Volume:    decimal.NewFromFloat(c.Volume),
	}

	h.window = append(h.window, o)
	if len(h.window) > h.windowSize {
		h.window = h.window[len(h.window)-h.windowSize:]
	}
}

func (h *CandleSignalHandler) calculateAndSend(ctx context.Context) {
	if len(h.window) == 0 {
		return
	}
	values, err := h.registry.CalculateAll(ctx, h.window)
	if err != nil {
		log.Printf("indicators calc error: %v", err)
	}

	// Формируем краткое сообщение
	lastTs := h.window[len(h.window)-1].Timestamp.Format(time.RFC3339)
	msg := fmt.Sprintf("[%s %s] ", h.symbol, lastTs)
	first := true
	for name, v := range values {
		if !first {
			msg += ", "
		} else {
			first = false
		}
		msg += fmt.Sprintf("%s=%.6f", name, v.Value.InexactFloat64())
	}
	if h.chatBot != nil {
		_ = h.chatBot.SendText(msg)
	} else {
		log.Printf("indicators: %s", msg)
	}
}

// backfillRecent загружает последние count свечей из REST и заполняет окно
func (h *CandleSignalHandler) backfillRecent(ctx context.Context, count int) error {
	log.Printf("rest backfill recent symbol=%s bin=%s count=%d", h.symbol, h.binSize, count)
	candles, err := h.fetchCandlesREST(ctx, h.symbol, h.binSize, count, true, time.Time{}, time.Time{})
	if err != nil {
		return err
	}
	// reverse=true — от новых к старым; загрузим в возрастающем порядке
	for i := len(candles) - 1; i >= 0; i-- {
		h.pushCandle(candles[i])
	}
	h.calculateAndSend(ctx)
	return nil
}

// backfillIfGap догружает пропущенные свечи между последней в окне и новой свечой
func (h *CandleSignalHandler) backfillIfGap(ctx context.Context, next Candle) {
	if len(h.window) == 0 {
		return
	}
	lastTs := h.window[len(h.window)-1].Timestamp.Unix()
	interval := h.binSizeSeconds()
	if interval <= 0 {
		return
	}
	gap := next.Timestamp - lastTs
	if gap <= int64(interval) {
		return
	}
	log.Printf("gap detected: last=%d next=%d binSec=%d => backfill", lastTs, next.Timestamp, interval)
	start := time.Unix(lastTs+int64(interval), 0).UTC()
	end := time.Unix(next.Timestamp-int64(interval), 0).UTC()
	candles, err := h.fetchCandlesREST(ctx, h.symbol, h.binSize, 0, false, start, end)
	if err != nil {
		log.Printf("rest backfill gap error: %v", err)
		if h.chatBot != nil {
			_ = h.chatBot.SendText(fmt.Sprintf("REST gap backfill error: %v", err))
		}
		return
	}
	for _, c := range candles {
		h.pushCandle(c)
	}
}

// fetchCandlesREST запрашивает свечи из REST BitMEX
func (h *CandleSignalHandler) fetchCandlesREST(ctx context.Context, symbol, binSize string, count int, reverse bool, start, end time.Time) ([]Candle, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	endpoint := h.baseURL + "/api/v1/trade/bucketed"
	q := url.Values{}
	q.Set("binSize", binSize)
	q.Set("symbol", symbol)
	if count > 0 {
		q.Set("count", fmt.Sprintf("%d", count))
	}
	if reverse {
		q.Set("reverse", "true")
	}
	if !start.IsZero() {
		q.Set("startTime", start.Format(time.RFC3339))
	}
	if !end.IsZero() {
		q.Set("endTime", end.Format(time.RFC3339))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bitmex http %d", resp.StatusCode)
	}
	type restCandle struct {
		Timestamp time.Time `json:"timestamp"`
		Open      float64   `json:"open"`
		High      float64   `json:"high"`
		Low       float64   `json:"low"`
		Close     float64   `json:"close"`
		Volume    float64   `json:"volume"`
	}
	var arr []restCandle
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&arr); err != nil {
		return nil, err
	}
	out := make([]Candle, 0, len(arr))
	for _, c := range arr {
		out = append(out, Candle{
			Timestamp: c.Timestamp.Unix(),
			Open:      c.Open,
			High:      c.High,
			Low:       c.Low,
			Close:     c.Close,
			Volume:    c.Volume,
		})
	}
	return out, nil
}

func (h *CandleSignalHandler) binSizeSeconds() int {
	switch h.binSize {
	case "1m":
		return 60
	case "5m":
		return 300
	case "15m":
		return 900
	case "1h":
		return 3600
	case "1d":
		return 86400
	default:
		return 60
	}
}
