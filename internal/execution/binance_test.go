package execution

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDecimalQuantity(t *testing.T) {
	for _, tt := range []struct{ q, step, want string }{{"0.123456789", "0.001", "0.123"}, {"1.999999999", "0.1", "1.9"}, {"0.999", "0.005", "0.995"}, {"10", "1", "10"}} {
		got, e := Quantize(tt.q, tt.step)
		if e != nil || got != tt.want {
			t.Fatalf("%s %s => %s %v", tt.q, tt.step, got, e)
		}
	}
	if _, e := Quantize("NaN", "0.1"); e == nil {
		t.Fatal("NaN accepted")
	}
}
func TestSignedReduceOnlyOrder(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fapi/v1/time" {
			json.NewEncoder(w).Encode(map[string]int64{"serverTime": 1770000000000})
			return
		}
		posts++
		if r.Method != "POST" || r.URL.Path != "/fapi/v1/order" {
			t.Error("unexpected endpoint")
		}
		r.ParseForm()
		q := r.PostForm
		signature := q.Get("signature")
		q.Del("signature")
		mac := hmac.New(sha256.New, []byte("secret"))
		mac.Write([]byte(q.Encode()))
		if signature != hex.EncodeToString(mac.Sum(nil)) {
			t.Error("invalid signature")
		}
		if r.Header.Get("X-MBX-APIKEY") != "test-key" || q.Get("reduceOnly") != "true" || q.Get("newClientOrderId") != "test-order" {
			t.Error("missing order safeguards")
		}
		json.NewEncoder(w).Encode(Receipt{Status: "FILLED", ClientID: "test-order", ExecutedQty: "0.01", AvgPrice: "100"})
	}))
	defer server.Close()
	b := Binance{BaseURL: server.URL, Key: "test-key", Secret: "secret", Client: server.Client()}
	r, e := b.Submit(context.Background(), Order{Symbol: "BTCUSDT", Side: "SELL", Quantity: "0.01", ClientID: "test-order", ReduceOnly: true})
	if e != nil || r.Status != "FILLED" || posts != 1 {
		t.Fatalf("%+v %v", r, e)
	}
}

func TestMarketLotFiltersAndNotionalLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/exchangeInfo" {
			t.Fatal("unexpected request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","quoteAsset":"USDT","contractType":"PERPETUAL","filters":[{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001","maxQty":"100"},{"filterType":"MARKET_LOT_SIZE","stepSize":"0.01","minQty":"0.01","maxQty":"10"},{"filterType":"MIN_NOTIONAL","notional":"5"}]}]}`))
	}))
	defer server.Close()
	b := Binance{BaseURL: server.URL, Client: server.Client(), MaxNotional: 100}
	for _, tt := range []struct {
		quantity      string
		reduce, valid bool
		want          string
	}{
		{"0.1234", false, true, "0.12"}, {"2", false, false, ""}, {"0.01", false, false, ""}, {"0.001", false, false, ""}, {"0.01", true, true, "0.01"}, {"0.015", true, false, ""},
	} {
		o, e := b.Prepare(context.Background(), Order{Symbol: "BTCUSDT", Side: "BUY", Quantity: tt.quantity, ReduceOnly: tt.reduce, ClientID: "filter-test"}, 100)
		if tt.valid {
			if e != nil || o.Quantity != tt.want {
				t.Fatalf("%+v -> %+v %v", tt, o, e)
			}
		} else if e == nil {
			t.Fatalf("unsafe order accepted: %+v", tt)
		}
	}
}
