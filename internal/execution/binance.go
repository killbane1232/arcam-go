package execution

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"arcam/internal/core"
)

type Order struct {
	Symbol     string `json:"symbol"`
	Side       string `json:"side"`
	Quantity   string `json:"quantity"`
	ClientID   string `json:"client_id"`
	ReduceOnly bool   `json:"reduce_only"`
}
type Receipt struct {
	OrderID     int64  `json:"orderId"`
	ClientID    string `json:"clientOrderId"`
	Status      string `json:"status"`
	ExecutedQty string `json:"executedQty"`
	AvgPrice    string `json:"avgPrice"`
}

// Broker can be implemented by other exchanges without changing strategy logic.
type Broker interface {
	Position(context.Context, string) (float64, error)
	Quote(context.Context, string) (float64, error)
	Prepare(context.Context, Order, float64) (Order, error)
	Submit(context.Context, Order) (Receipt, error)
	Lookup(context.Context, Order) (Receipt, error)
	CheckAccount(context.Context, []string) error
}
type Binance struct {
	BaseURL, Key, Secret string
	Client               *http.Client
	MaxNotional          float64
}

func (b *Binance) request(ctx context.Context, method, path string, q url.Values, signed bool, out any) error {
	if q == nil {
		q = url.Values{}
	}
	if signed {
		if b.Key == "" || b.Secret == "" {
			return fmt.Errorf("Binance API credentials are not configured")
		}
		var clock struct {
			Time int64 `json:"serverTime"`
		}
		if e := b.request(ctx, "GET", "/fapi/v1/time", nil, false, &clock); e != nil {
			return e
		}
		if clock.Time <= 0 {
			return fmt.Errorf("invalid exchange time")
		}
		q.Set("timestamp", strconv.FormatInt(clock.Time, 10))
		q.Set("recvWindow", "5000")
		mac := hmac.New(sha256.New, []byte(b.Secret))
		mac.Write([]byte(q.Encode()))
		q.Set("signature", hex.EncodeToString(mac.Sum(nil)))
	}
	endpoint := b.BaseURL + path
	var body io.Reader
	if method == "GET" {
		endpoint += "?" + q.Encode()
	} else {
		body = strings.NewReader(q.Encode())
	}
	req, e := http.NewRequestWithContext(ctx, method, endpoint, body)
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if signed {
		req.Header.Set("X-MBX-APIKEY", b.Key)
	}
	res, e := b.Client.Do(req)
	if e != nil {
		return fmt.Errorf("exchange transport error; order state may be unknown")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		var payload struct {
			Code int `json:"code"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&payload)
		return fmt.Errorf("exchange HTTP %d code %d", res.StatusCode, payload.Code)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
}
func (b *Binance) Quote(ctx context.Context, symbol string) (float64, error) {
	var q struct {
		Price string `json:"price"`
	}
	e := b.request(ctx, "GET", "/fapi/v1/ticker/price", url.Values{"symbol": {symbol}}, false, &q)
	if e != nil {
		return 0, e
	}
	p, e := strconv.ParseFloat(q.Price, 64)
	if e != nil || !core.Finite(p) || p <= 0 {
		return 0, fmt.Errorf("invalid exchange price")
	}
	return p, nil
}
func (b *Binance) Position(ctx context.Context, symbol string) (float64, error) {
	var p []struct {
		Symbol string `json:"symbol"`
		Side   string `json:"positionSide"`
		Amount string `json:"positionAmt"`
	}
	if e := b.request(ctx, "GET", "/fapi/v3/positionRisk", url.Values{"symbol": {symbol}}, true, &p); e != nil {
		return 0, e
	}
	qty := 0.0
	for _, v := range p {
		if v.Symbol != symbol {
			continue
		}
		if v.Side != "BOTH" {
			return 0, fmt.Errorf("only one-way position mode is supported")
		}
		n, e := strconv.ParseFloat(v.Amount, 64)
		if e != nil || !core.Finite(n) {
			return 0, fmt.Errorf("invalid position quantity")
		}
		qty += n
	}
	return qty, nil
}
func (b *Binance) CheckAccount(ctx context.Context, symbols []string) error {
	var mode struct {
		Dual bool `json:"dualSidePosition"`
	}
	if e := b.request(ctx, "GET", "/fapi/v1/positionSide/dual", nil, true, &mode); e != nil {
		return e
	}
	if mode.Dual {
		return fmt.Errorf("Binance account must be in one-way mode")
	}
	for _, s := range symbols {
		var orders []json.RawMessage
		if e := b.request(ctx, "GET", "/fapi/v1/openOrders", url.Values{"symbol": {s}}, true, &orders); e != nil {
			return e
		}
		if len(orders) > 0 {
			return fmt.Errorf("%s has external open orders", s)
		}
	}
	return nil
}
func (b *Binance) Prepare(ctx context.Context, o Order, quote float64) (Order, error) {
	if !core.SymbolPattern.MatchString(o.Symbol) || (o.Side != "BUY" && o.Side != "SELL") || len(o.ClientID) > 36 || !core.IDPattern.MatchString(o.ClientID) {
		return o, fmt.Errorf("invalid order")
	}
	var info struct {
		Symbols []struct {
			Symbol     string `json:"symbol"`
			Status     string `json:"status"`
			QuoteAsset string `json:"quoteAsset"`
			Contract   string `json:"contractType"`
			Filters    []struct {
				Type     string `json:"filterType"`
				Step     string `json:"stepSize"`
				Min      string `json:"minQty"`
				Max      string `json:"maxQty"`
				Notional string `json:"notional"`
			} `json:"filters"`
		} `json:"symbols"`
	}
	if e := b.request(ctx, "GET", "/fapi/v1/exchangeInfo", nil, false, &info); e != nil {
		return o, e
	}
	for _, s := range info.Symbols {
		if s.Symbol != o.Symbol {
			continue
		}
		if s.Status != "TRADING" || s.QuoteAsset != "USDT" || s.Contract != "PERPETUAL" {
			return o, fmt.Errorf("only trading USDT perpetuals are supported")
		}
		step, min, max, minNotional := "", "", "", 0.0
		for _, f := range s.Filters {
			if f.Type == "LOT_SIZE" && step == "" {
				step, min, max = f.Step, f.Min, f.Max
			}
			if f.Type == "MARKET_LOT_SIZE" && f.Step != "0" && f.Step != "0.00000000" {
				step, min, max = f.Step, f.Min, f.Max
			}
			if f.Type == "MIN_NOTIONAL" {
				var e error
				minNotional, e = strconv.ParseFloat(f.Notional, 64)
				if e != nil {
					return o, e
				}
			}
		}
		if step == "" {
			return o, fmt.Errorf("missing exchange quantity filters")
		}
		qty, e := Quantize(o.Quantity, step)
		if e != nil {
			return o, e
		}
		q, e := strconv.ParseFloat(qty, 64)
		if e != nil {
			return o, e
		}
		lo, e := strconv.ParseFloat(min, 64)
		if e != nil {
			return o, e
		}
		hi, e := strconv.ParseFloat(max, 64)
		if e != nil {
			return o, e
		}
		if q <= 0 || q < lo || q > hi {
			return o, fmt.Errorf("quantity outside exchange lot limits")
		}
		if !core.Finite(quote) || quote <= 0 {
			return o, fmt.Errorf("invalid quote")
		}
		if !o.ReduceOnly && (q*quote < minNotional || q*quote > b.MaxNotional) {
			return o, fmt.Errorf("notional outside exchange minimum or configured maximum")
		}
		if o.ReduceOnly {
			original, e := strconv.ParseFloat(o.Quantity, 64)
			if e != nil || math.Abs(q-original) > 1e-12 {
				return o, fmt.Errorf("reduce-only quantity is not aligned to exchange step")
			}
		}
		o.Quantity = qty
		return o, nil
	}
	return o, fmt.Errorf("unknown symbol")
}

// Decimal arithmetic prevents a float conversion from rounding quantity UP.
func Quantize(quantity, step string) (string, error) {
	q, ok := new(big.Rat).SetString(quantity)
	if !ok || q.Sign() <= 0 {
		return "", fmt.Errorf("invalid quantity")
	}
	s, ok := new(big.Rat).SetString(step)
	if !ok || s.Sign() <= 0 {
		return "", fmt.Errorf("invalid step")
	}
	ratio := new(big.Rat).Quo(q, s)
	units := new(big.Int).Quo(ratio.Num(), ratio.Denom())
	result := new(big.Rat).Mul(new(big.Rat).SetInt(units), s)
	precision := 0
	if idx := strings.IndexByte(step, '.'); idx >= 0 {
		precision = len(step) - idx - 1
	}
	return result.FloatString(precision), nil
}
func (b *Binance) Submit(ctx context.Context, o Order) (Receipt, error) {
	var r Receipt
	q := url.Values{"symbol": {o.Symbol}, "side": {o.Side}, "type": {"MARKET"}, "quantity": {o.Quantity}, "newClientOrderId": {o.ClientID}, "newOrderRespType": {"RESULT"}, "reduceOnly": {strconv.FormatBool(o.ReduceOnly)}}
	e := b.request(ctx, "POST", "/fapi/v1/order", q, true, &r)
	return r, e
}
func (b *Binance) Lookup(ctx context.Context, o Order) (Receipt, error) {
	var r Receipt
	e := b.request(ctx, "GET", "/fapi/v1/order", url.Values{"symbol": {o.Symbol}, "origClientOrderId": {o.ClientID}}, true, &r)
	return r, e
}
