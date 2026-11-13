package market

import (
	"context"
	"encoding/json"
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
