package market

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPaginationAndCSV(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		ts, _ := strconv.ParseInt(r.URL.Query().Get("startTime"), 10, 64)
		rows := [][]any{}
		for i := 0; i < 2 && ts < start.Add(3*time.Hour).UnixMilli(); i++ {
			rows = append(rows, []any{ts, "100", "110", "90", "105", "10", ts + 3599999})
			ts += 3600000
		}
		json.NewEncoder(w).Encode(rows)
	}))
	defer server.Close()
	b := &Binance{BaseURL: server.URL, Prefix: "/api/v3", Client: server.Client()}
	bars, e := b.Candles(context.Background(), "BTCUSDT", "1h", start, start.Add(3*time.Hour))
	if e != nil || len(bars) != 3 || calls != 2 {
		t.Fatalf("pagination %d %d %v", len(bars), calls, e)
	}
	var csv strings.Builder
	if e = WriteCSV(&csv, bars); e != nil {
		t.Fatal(e)
	}
	d, e := ReadCSV(strings.NewReader(csv.String()), "csv", "BTCUSDT", "1h")
	if e != nil || len(d.Candles) != 3 {
		t.Fatal(e)
	}
	if _, e = ReadCSV(strings.NewReader("time,open,high,low,close,volume\n2025-01-01T00:00:00Z,1,2,3,4,5\n"), "csv", "BTCUSDT", "1h"); e == nil {
		t.Fatal("invalid OHLC accepted")
	}
}

type timeframeTransport func(*http.Request) (*http.Response, error)

func (f timeframeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBinanceOnlyRequestsAndAcceptsClosedTimeframes(t *testing.T) {
	for _, interval := range []string{"15m", "4h", "1d"} {
		t.Run(interval, func(t *testing.T) {
			durations := map[string]time.Duration{"15m": 15 * time.Minute, "4h": 4 * time.Hour, "1d": 24 * time.Hour}
			step := durations[interval]
			end := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
			calls := 0
			includeUnclosed := false
			client := &http.Client{Transport: timeframeTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Query().Get("endTime") != strconv.FormatInt(end.UnixMilli()-1, 10) || r.URL.Query().Get("startTime") != strconv.FormatInt(end.Add(-step).UnixMilli(), 10) {
					t.Fatal("HTTP request included forming timeframe", r.URL.RawQuery)
				}
				at := end.Add(-step)
				if includeUnclosed {
					at = end
				}
				encoded, _ := json.Marshal([][]any{{at.UnixMilli(), "100", "110", "90", "105", "10", at.Add(step).UnixMilli() - 1}})
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
			})}
			b := &Binance{BaseURL: "https://market.invalid", Prefix: "/api/v3", Client: client}
			formingEnd := time.Now().UTC().Truncate(step).Add(step)
			if _, e := b.Candles(context.Background(), "BTCUSDT", interval, formingEnd.Add(-step), formingEnd); e == nil || calls != 0 {
				t.Fatal("requested an unfinished timeframe")
			}
			bars, e := b.Candles(context.Background(), "BTCUSDT", interval, end.Add(-step), end)
			if e != nil || len(bars) != 1 || !bars[0].Time.Add(step).Equal(end) {
				t.Fatal("closed candle rejected", e)
			}
			includeUnclosed = true
			if _, e = b.Candles(context.Background(), "BTCUSDT", interval, end.Add(-step), end); e == nil {
				t.Fatal("accepted a forming candle from exchange response")
			}
		})
	}
}
