package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"arcam-go/pkg/config"

	ind "arcam-go/internal/service/indicator"
	md "arcam-go/internal/service/market_data"
	tb "arcam-go/internal/service/telebot"

	"github.com/gorilla/websocket"
)

func main() {
	// Простая проверка запуска с graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	fmt.Println("apex-trader starting...")
	cfg, err := config.Load("configs/config.yaml")
	if err != nil {
		log.Fatalf("config load error: %v", err)
	}

	if cfg.Candles.Enabled {
		if err := dumpLastCandlesWS(ctx, cfg); err != nil {
			log.Fatalf("candles dump error: %v", err)
		}
		fmt.Println("candles dump completed")
		// не выходим: ниже демонстрация обработчика сигналов
	}

	// Инициализация Telegram-бота (опционально)
	var bot *tb.TeleBot
	if cfg.Telebot.Token != "" {
		b, err := tb.NewTeleBot(tb.BotConfig{Token: cfg.Telebot.Token, ChatID: cfg.Telebot.ChatID}, nil, nil)
		if err != nil {
			log.Printf("telebot init error: %v", err)
		} else {
			bot = b
			go bot.Start(ctx) // запустим polling
		}
	}

	// Реестр индикаторов: EMA(20), Aroon(14)
	registry := ind.NewRegistry(ind.DefaultFactory{})
	_ = registry.Register(ind.IndicatorConfig{Type: ind.IndicatorTypeEMA, Period: 20, Name: "ema_20"})
	_ = registry.Register(ind.IndicatorConfig{Type: ind.IndicatorTypeAroon, Period: 14, Name: "aroon_14"})

	// Стрим BitMEX (реальное WS-подключение)
	stream := md.NewBitmexStream(cfg.Bitmex.WSURL, cfg.Candles.BinSize)
	handler := md.NewCandleSignalHandler(stream, registry, cfg.Candles.Symbol, bot, cfg.Bitmex.BaseURL, cfg.Candles.BinSize, 200)
	if err := handler.Start(ctx); err != nil {
		log.Printf("handler start error: %v", err)
	}

	// Держим процесс активным до завершения контекста
	<-ctx.Done()
	select {
	case <-ctx.Done():
		fmt.Println("apex-trader shutdown signal received")
	case <-time.After(200 * time.Millisecond):
		// имитация краткого старта
	}
	_ = os.Getenv("BITMEX_API_KEY") // чтобы убедиться, что окружение читается
	fmt.Println("apex-trader exited")
}

type bitmexCandle struct {
	Timestamp time.Time `json:"timestamp"`
	Open      float64   `json:"open"`
	High      float64   `json:"high"`
	Low       float64   `json:"low"`
	Close     float64   `json:"close"`
	Volume    float64   `json:"volume"`
}

func dumpLastCandles(ctx context.Context, cfg *config.Config) error {
	client := &http.Client{Timeout: 10 * time.Second}
	endpoint := cfg.Bitmex.BaseURL + "/api/v1/trade/bucketed"
	q := url.Values{}
	q.Set("binSize", cfg.Candles.BinSize)
	q.Set("partial", "false")
	q.Set("symbol", cfg.Candles.Symbol)
	count := cfg.Candles.Count
	if count <= 0 {
		count = 10
	}
	q.Set("count", fmt.Sprintf("%d", count))
	if cfg.Candles.Reverse {
		q.Set("reverse", "true")
	} else {
		q.Set("reverse", "false")
	}
	fullURL := endpoint + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bitmex http %d: %s", resp.StatusCode, string(b))
	}
	var candles []bitmexCandle
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&candles); err != nil {
		return err
	}

	out := cfg.Candles.Output
	if out == "" {
		out = "candles.csv"
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil && filepath.Dir(out) != "." {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	// header
	if err := w.Write([]string{"timestamp", "open", "high", "low", "close", "volume"}); err != nil {
		return err
	}
	for _, c := range candles {
		row := []string{
			c.Timestamp.Format(time.RFC3339Nano),
			fmt.Sprintf("%g", c.Open),
			fmt.Sprintf("%g", c.High),
			fmt.Sprintf("%g", c.Low),
			fmt.Sprintf("%g", c.Close),
			fmt.Sprintf("%g", c.Volume),
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}

// dumpLastCandlesWS подключается к BitMEX WebSocket и выгружает последние N свечей
func dumpLastCandlesWS(ctx context.Context, cfg *config.Config) error {
	wsURL := cfg.Bitmex.WSURL
	if wsURL == "" {
		wsURL = "wss://www.bitmex.com/realtime"
	}

	d := &websocket.Dialer{
		HandshakeTimeout:  15 * time.Second,
		EnableCompression: false,
		ReadBufferSize:    1024,
		WriteBufferSize:   1024,
	}
	conn, _, err := d.DialContext(ctx, wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Пинг/понг, чтобы поддерживать соединение и избегать таймаутов
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	})
	pingTicker := time.NewTicker(5 * time.Second)
	defer pingTicker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-pingTicker.C:
				_ = conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(3*time.Second))
			}
		}
	}()

	// Подписка на канал tradeBin{binSize}:{symbol}
	topic := fmt.Sprintf("tradeBin%s:%s", cfg.Candles.BinSize, cfg.Candles.Symbol)
	sub := map[string]any{
		"op":   "subscribe",
		"args": []string{topic},
	}
	if err := conn.WriteJSON(sub); err != nil {
		return err
	}

	type candle struct {
		Timestamp time.Time `json:"timestamp"`
		Open      float64   `json:"open"`
		High      float64   `json:"high"`
		Low       float64   `json:"low"`
		Close     float64   `json:"close"`
		Volume    float64   `json:"volume"`
	}
	type wsResp struct {
		Table  string   `json:"table"`
		Action string   `json:"action"`
		Data   []candle `json:"data"`
	}

	need := cfg.Candles.Count
	if need <= 0 {
		need = 10
	}
	collected := make([]candle, 0, need)

	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()

	for len(collected) < need {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if len(collected) == 0 {
				return fmt.Errorf("ws timeout, no data received")
			}
			// Прервём по таймауту с тем, что есть
			break
		default:
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var resp wsResp
		if err := json.Unmarshal(msg, &resp); err != nil {
			// игнорируем нерелевантные сообщения (например, инфо/субскрипшн-акк)
			continue
		}
		if resp.Table == "" || len(resp.Data) == 0 {
			continue
		}
		// Поддерживаем partial (snapshot) и insert (инкремент)
		if resp.Action == "partial" || resp.Action == "insert" {
			collected = append(collected, resp.Data...)
			if len(collected) > need {
				collected = collected[len(collected)-need:]
			}
		}
	}

	out := cfg.Candles.Output
	if out == "" {
		out = "candles.csv"
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil && filepath.Dir(out) != "." {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"timestamp", "open", "high", "low", "close", "volume"}); err != nil {
		return err
	}
	for _, c := range collected {
		row := []string{
			c.Timestamp.Format(time.RFC3339Nano),
			fmt.Sprintf("%g", c.Open),
			fmt.Sprintf("%g", c.High),
			fmt.Sprintf("%g", c.Low),
			fmt.Sprintf("%g", c.Close),
			fmt.Sprintf("%g", c.Volume),
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}
